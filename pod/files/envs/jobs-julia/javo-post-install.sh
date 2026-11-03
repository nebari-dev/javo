# IJulia, the Julia kernel papermill runs notebooks with, is a Julia package
# from Julia's General registry, not conda-forge. Pkg installs exactly the
# versions in Manifest.toml (each checked against its git-tree-sha1) into
# this environment's Julia depot and precompiles them; the kernel spec
# "julia" goes into the environment's Jupyter data folder, where papermill
# finds it. conda-forge's julia activation keeps the depot and the default
# project under $CONDA_PREFIX/share/julia. internal/pixienv runs this file in
# the activated environment, once per lockfile.
set -euo pipefail
# conda-forge's julia unpacks archives with a 7z that cannot read the zstd
# tarball Pkg keeps the General registry in, so the registry is unpacked into
# a folder; the template runs julia with the same setting.
export JULIA_PKG_UNPACK_REGISTRY=true
# IJulia depends on Conda.jl, whose build refuses the empty CONDA_JL_CONDA_EXE
# that conda-forge's julia activation sets (there is no conda here); without
# it Conda.jl builds and installs nothing, and IJulia never needs it, since
# the environment has Jupyter.
unset CONDA_JL_CONDA_EXE CONDA_JL_HOME
# IJulia's build would add a kernel spec to the home; the one below, in the
# environment, is the only one.
export IJULIA_NODEFAULTKERNEL=true JUPYTER_DATA_DIR=$CONDA_PREFIX/share/jupyter
project=$CONDA_PREFIX/share/julia/environments/${JULIA_PROJECT#@}
mkdir -p "$project"
cp Project.toml Manifest.toml LocalPreferences.toml "$project/"
JULIA_PKG_PRECOMPILE_AUTO=0 julia -e 'using Pkg; Pkg.instantiate()'
julia -e 'using Pkg; Pkg.precompile(; strict=true)'
julia -e 'using IJulia; installkernel("Julia"; specname="julia", env=Dict("JULIA_PKG_UNPACK_REGISTRY" => "true"))'
