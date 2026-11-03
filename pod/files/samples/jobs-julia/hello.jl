# The sample job of the jobs-julia template ("Say hello in Julia"). javo runs
# it as a command job,
#   julia /opt/javo/samples/jobs-julia/hello.jl
# in the person's home folder, with each parameter as JAVO_PARAM_<NAME>.
using Dates

name = get(ENV, "JAVO_PARAM_NAME", "world")
println("Hello, $(name), from Julia $(VERSION)")
println("The time is $(Dates.format(now(UTC), "yyyy-mm-dd HH:MM:SS")) UTC")
println("The squares of 1 to 5: $(join([i^2 for i in 1:5], ", "))")
