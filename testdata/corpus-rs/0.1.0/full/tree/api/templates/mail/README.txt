Create a mailer with spinup g mailer welcome name:string.
Edit welcome.subject.tmpl, welcome.txt.tmpl, and welcome.html.tmpl.
Use minijinja expressions such as {{ name }} for template data.
The text template is required. The HTML template escapes its data.
With jobs enabled, api worker sends queued mail with ten attempts.
The standard worker consumes mail and application jobs from the default queue.
