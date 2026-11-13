//go:build local

package wiring

// LocalBuild says whether this binary was built with the tag local
// (make local): this one was. It carries what `javo local` runs, apps
// as processes of one person's machine, and leaves out what that never
// reaches so that it is small to download. This constant is the only
// thing that decides it: each place that starts something left out asks
// it first and answers with LeftOut, and since it is a constant the
// compiler drops what follows and the linker everything only that used.
const LocalBuild = true
