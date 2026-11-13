//go:build local

package buildinfo

// Local says whether this binary was built with the tag local
// (make local): this one was. It carries what `javo local` runs, apps
// as processes of one person's machine, and leaves out what that never
// reaches so that it is small to download. What is left out is in files
// that build does not compile (//go:build !local), each with a stand-in
// that answers with LeftOut, so nothing left out can be linked by
// mistake. This constant only lets a command refuse early, in the words
// of the build, what a stand-in would refuse later.
const Local = true
