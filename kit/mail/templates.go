package mail

import (
	"bytes"
	"fmt"
	htmltemplate "html/template"
	"io/fs"
	"strings"
	"text/template"
)

// Render renders one subject, txt, and html template set from files.
// The file names are <name>.{subject,txt,html}.tmpl. HTML uses contextual escaping.
func Render(files fs.FS, name, from, to string, data any) (Message, error) {
	m := Message{From: from, To: to}
	if files == nil || !fs.ValidPath(name) || strings.Contains(name, "/") || name == "." {
		return m, fmt.Errorf("mail: cannot render template: invalid name\nTry: use a template base name without a path")
	}
	for _, part := range []struct {
		suffix string
		dst    *string
		html   bool
	}{
		{"subject", &m.Subject, false}, {"txt", &m.Text, false}, {"html", &m.HTML, true},
	} {
		file := name + "." + part.suffix + ".tmpl"
		source, err := fs.ReadFile(files, file)
		if err != nil {
			return Message{}, fmt.Errorf("mail: cannot read template %s: %w\nTry: embed all three templates", file, err)
		}
		var buf bytes.Buffer
		if part.html {
			var tmpl *htmltemplate.Template
			tmpl, err = htmltemplate.New(file).Option("missingkey=error").Parse(string(source))
			if err == nil {
				err = tmpl.Execute(&buf, data)
			}
		} else {
			var tmpl *template.Template
			tmpl, err = template.New(file).Option("missingkey=error").Parse(string(source))
			if err == nil {
				err = tmpl.Execute(&buf, data)
			}
		}
		if err != nil {
			return Message{}, fmt.Errorf("mail: cannot render template %s\nTry: check the template syntax and data fields", file)
		}
		*part.dst = buf.String()
	}
	m.Subject = strings.TrimSpace(m.Subject)
	if err := validate(m); err != nil {
		return Message{}, err
	}
	return m, nil
}
