// Package gendocs provides the gendocs command.
package gendocs

import (
	"bytes"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"
	"time"

	"github.com/rclone/rclone/cmd"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config/flags"
	"github.com/rclone/rclone/lib/file"
	"github.com/spf13/cobra"
	"github.com/spf13/cobra/doc"
)

func init() {
	cmd.Root.AddCommand(commandDefinition)
}

// define things which go into the frontmatter
type frontmatter struct {
	Date        string
	Title       string
	Description string
	Source      string
	Aliases     []string
	Annotations map[string]string
}

var frontmatterTemplate = template.Must(template.New("frontmatter").Parse(`---
title: "{{ .Title }}"
description: "{{ .Description }}"
{{- if .Aliases }}
aliases:
{{- range $value := .Aliases }}
  - {{ $value }}
{{- end }}
{{- end }}
{{- range $key, $value := .Annotations }}
{{ $key }}: {{  $value }}
{{- end }}
# autogenerated - DO NOT EDIT, instead edit the source code in {{ .Source }} and as part of making a release run "make commanddocs"
---
`))

var commandDefinition = &cobra.Command{
	Use:   "gendocs output_directory",
	Short: `Output markdown docs for rclone to the directory supplied.`,
	Long: `This produces markdown docs for the rclone commands to the directory
supplied.  These are in a format suitable for hugo to render into the
rclone.org website.`,
	Annotations: map[string]string{
		"versionIntroduced": "v1.33",
	},
	RunE: func(command *cobra.Command, args []string) error {
		cmd.CheckArgs(1, 1, command, args)
		now := time.Now().Format(time.RFC3339)

		// Create the directory structure
		root := args[0]
		out := filepath.Join(root, "commands")
		err := file.MkdirAll(out, 0777)
		if err != nil {
			return err
		}

		// Write the flags page
		var buf bytes.Buffer
		cmd.Root.SetOutput(&buf)
		cmd.Root.SetArgs([]string{"help", "flags"})
		cmd.GeneratingDocs = true
		err = cmd.Root.Execute()
		if err != nil {
			return err
		}
		err = os.WriteFile(filepath.Join(root, "flags.md"), buf.Bytes(), 0777)
		if err != nil {
			return err
		}

		// Look up name => details for prepender
		type commandDetails struct {
			Short       string
			Aliases     []string
			Annotations map[string]string
		}
		var commands = map[string]commandDetails{}
		var addCommandDetails func(root *cobra.Command, parentAliases []string)
		addCommandDetails = func(root *cobra.Command, parentAliases []string) {
			name := strings.ReplaceAll(root.CommandPath(), " ", "_") + ".md"
			var aliases []string
			for _, p := range parentAliases {
				aliases = append(aliases, p+" "+root.Name())
				for _, v := range root.Aliases {
					aliases = append(aliases, p+" "+v)
				}
			}
			for _, v := range root.Aliases {
				if root.HasParent() {
					aliases = append(aliases, root.Parent().CommandPath()+" "+v)
				} else {
					aliases = append(aliases, v)
				}
			}
			commands[name] = commandDetails{
				Short:       root.Short,
				Aliases:     aliases,
				Annotations: root.Annotations,
			}
			for _, c := range root.Commands() {
				addCommandDetails(c, aliases)
			}
		}
		addCommandDetails(cmd.Root, []string{})

		// markup for the docs files
		prepender := func(filename string) string {
			name := filepath.Base(filename)
			base := strings.TrimSuffix(name, path.Ext(name))
			data := frontmatter{
				Date:        now,
				Title:       strings.ReplaceAll(base, "_", " "),
				Description: commands[name].Short,
				Source:      strings.ReplaceAll(strings.ReplaceAll(base, "rclone", "cmd"), "_", "/") + "/",
				Aliases:     []string{},
				Annotations: map[string]string{},
			}
			for _, v := range commands[name].Aliases {
				data.Aliases = append(data.Aliases, "/commands/"+strings.ReplaceAll(v, " ", "_")+"/")
			}
			// Filter out annotations that confuse hugo from the frontmatter
			for k, v := range commands[name].Annotations {
				if k != "groups" {
					data.Annotations[k] = v
				}
			}
			var buf bytes.Buffer
			err := frontmatterTemplate.Execute(&buf, data)
			if err != nil {
				fs.Fatalf(nil, "Failed to render frontmatter template: %v", err)
			}
			return buf.String()
		}
		linkHandler := func(name string) string {
			base := strings.TrimSuffix(name, path.Ext(name))
			return "/commands/" + strings.ToLower(base) + "/"
		}

		err = doc.GenMarkdownTreeCustom(cmd.Root, out, prepender, linkHandler)
		if err != nil {
			return err
		}

		var outdentTitle = regexp.MustCompile(`(?m)^#(#+)`)

		// Munge the files to add a link to the global flags page
		err = filepath.Walk(out, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if !info.IsDir() {
				name := filepath.Base(path)
				cmd, ok := commands[name]
				if !ok {
					return fmt.Errorf("didn't find command for %q", name)
				}
				b, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				doc := string(b)

				startCut := strings.Index(doc, `### Options inherited from parent commands`)
				endCut := strings.Index(doc, `### SEE ALSO`)
				if startCut < 0 || endCut < 0 {
					if name != "rclone.md" {
						return fmt.Errorf("internal error: failed to find cut points: startCut = %d, endCut = %d", startCut, endCut)
					}
					if endCut >= 0 {
						doc = doc[:endCut] + "### See Also" + doc[endCut+12:]
					}
				} else {
					var out strings.Builder
					if groupsString := cmd.Annotations["groups"]; groupsString != "" {
						_, _ = out.WriteString("Options shared with other commands are described next.\n")
						_, _ = out.WriteString("See the [global flags page](/flags/) for global options not listed here.\n\n")
						groups := flags.All.Include(groupsString)
						for _, group := range groups.Groups {
							if group.Flags.HasFlags() {
								_, _ = fmt.Fprintf(&out, "#### %s Options\n\n", group.Name)
								_, _ = fmt.Fprintf(&out, "%s\n\n", group.Help)
								_, _ = out.WriteString("```\n")
								_, _ = out.WriteString(group.Flags.FlagUsages())
								_, _ = out.WriteString("```\n\n")
							}
						}
					} else {
						_, _ = out.WriteString("See the [global flags page](/flags/) for global options not listed here.\n\n")
					}
					doc = doc[:startCut] + out.String() + "### See Also" + doc[endCut+12:]
				}

				// outdent all the titles by one
				doc = outdentTitle.ReplaceAllString(doc, `$1`)
				err = os.WriteFile(path, []byte(doc), 0777)
				if err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}

		return nil
	},
}
