# The sample job of the jobs-r template ("Say hello in R"). javo runs it as a
# command job,
#   Rscript /opt/javo/samples/jobs-r/hello.R
# in the person's home folder, with each parameter as JAVO_PARAM_<NAME>.
name <- Sys.getenv("JAVO_PARAM_NAME", "world")
cat(sprintf("Hello, %s, from %s\n", name, R.version.string))
cat(sprintf("The time is %s UTC\n", format(Sys.time(), "%Y-%m-%d %H:%M:%S", tz = "UTC")))
cat("Summary of 1 to 10:\n")
print(summary(1:10))
