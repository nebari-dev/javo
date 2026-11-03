# Pluto is a Julia package from Julia's General registry, not conda-forge.
# Pkg installs exactly the versions in Manifest.toml (each checked against
# its git-tree-sha1) into this environment's Julia depot, then precompiles
# them. conda-forge's julia activation keeps the depot and the default
# project under $CONDA_PREFIX/share/julia, so Pluto is what `julia` loads in
# this environment. internal/pixienv runs this file in the activated
# environment, once per lockfile.
set -euo pipefail
# conda-forge's julia unpacks archives with a 7z that cannot read the zstd
# tarball Pkg keeps the General registry in, so the registry is unpacked into
# a folder; the template runs julia with the same setting.
export JULIA_PKG_UNPACK_REGISTRY=true
project=$CONDA_PREFIX/share/julia/environments/${JULIA_PROJECT#@}
mkdir -p "$project"
cp Project.toml Manifest.toml "$project/"
JULIA_PKG_PRECOMPILE_AUTO=0 julia -e 'using Pkg; Pkg.instantiate()'
julia -e 'using Pkg; Pkg.precompile(; strict=true)'
