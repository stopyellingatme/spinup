// Package job is the `spinup g job <name>` generator. It adds a River job
// to api/internal/jobs/ (an args type with Kind, a worker function, and a
// test) and rewrites the generator-owned api/internal/jobs/register_gen.go,
// which registers every job of the package. The app needs the jobs pack.
package job
