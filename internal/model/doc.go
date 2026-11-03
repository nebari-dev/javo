// Package model holds the records javo keeps (apps, templates, jobs, runs,
// people), the names and labels it puts on cluster objects, and the rules a
// record must satisfy before it is saved. It has no storage and no
// Kubernetes client; internal/store and the controllers build on it.
package model
