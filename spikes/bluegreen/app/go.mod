// The blue/green spike app. It is its own module, so it does not join the
// spinup CLI module or go.work. run.sh builds it inside Docker only.
module bgspike

go 1.26.0
