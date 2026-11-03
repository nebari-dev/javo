---
title: Environments
description: The packages an app or job runs with. Templates install a pinned pixi environment into your home; a pixi.lock in your repository or a Nebi environment replaces it.
sidebar:
  order: 6
---

Javo does not build an image per framework. Apps and jobs run **upstream images, unmodified**: most templates run the [pixi](https://pixi.sh) image, and install a **pixi environment** into your home folder that holds Python, R or Julia and the template's packages. RStudio runs the Rocker project's image and static sites run nginx, both as published.

## Where an environment comes from

The first match wins:

1. **A Nebi environment** picked on New app or New job, when your admin has [set up Nebi](/admin/nebi/).
2. **Your repository's lockfile**: for an app that runs from Git, a `pixi.toml` and a `pixi.lock` at the root of the repository, when both are there.
3. **The template's own environment**: conda-forge packages pinned by a committed `pixi.lock`, shipped with Javo.

Templates that run an image you name (Container image) have no environment.

JupyterLab is the exception to "first match wins" for a Nebi environment: it serves kernels, so a picked Nebi environment is installed **beside** the template's own, and JupyterLab keeps running in the template's. See [JupyterLab and a picked environment](#jupyterlab-and-a-picked-environment).

## Install once, reuse after

On the first start, a short step before the app installs the environment exactly as its lockfile says (`pixi install --frozen`, no solving) into your home folder, under `~/.javo/envs/<hash of pixi.toml and pixi.lock>`, with pixi's download cache in `~/.javo/pixi-cache`. Every later start of any app or job with the same two files reuses it, so only the first start waits for downloads. An install that did not finish is cleared and done again on the next start.

The app's start-up log says which it did, for example `Installing the template's environment into ... (once for these files)` or `Using the repository's pixi.lock, installed before in ...`.

Inside the app, the environment is activated: its `bin` folder comes first on `PATH`, `CONDA_PREFIX` points at it, and its activation scripts have run.

:::note
A first start needs internet access from the pod: packages come from conda.anaconda.org, and for a few packages from pypi.org, registry.npmjs.org or pkg.julialang.org.
:::

## Bring your own lockfile

To run an app from Git with packages of your own, commit both files at the root of the repository:

```toml title="pixi.toml"
[workspace]
name = "sales-dashboard"
channels = ["conda-forge"]
platforms = ["linux-64", "linux-aarch64"]

[dependencies]
python = "3.13.*"
streamlit = "*"
pandas = "*"
```

Then run `pixi lock` and commit `pixi.lock` too. Things to know:

- The lockfile replaces the template's environment, so it must bring the program the template starts (here `streamlit`). Before the app starts, the install step checks that it does; if not, the app stops with **Environment lacks the program** and the sentence `The repository's pixi.lock has no streamlit. Add streamlit to it, or remove pixi.toml and pixi.lock from the repository.`
- The lockfile is installed as it is, so it needs the cluster's platform: `linux-64` (or `linux-aarch64` on Arm machines).
- A new commit with a changed lockfile installs a new environment on the next start; an unchanged one reuses the installed one.

## Nebi environments

With Nebi set up, New app and New job show an **Environment** card that lists your Nebi workspaces. Picking one copies its newest `pixi.toml` and `pixi.lock` into Javo with the app or job. To move to a newer version, create the app or job again. See [Nebi](/admin/nebi/) for the limits.

For every template except JupyterLab, the picked environment replaces the template's, so it must have the program the template starts (`streamlit`, `panel`, `python` for a custom command, and so on). If it does not, the app does not start and its page says, for example: `The environment smoke-pushed has no streamlit. Add streamlit to it in Nebi, or pick another environment.`

### JupyterLab and a picked environment

JupyterLab runs in the template's own environment; the picked one is installed next to it and:

- each Jupyter kernel it has (Python with `ipykernel`, R with `r-irkernel`, and so on) shows in the Launcher and the kernel picker under the environment's name;
- in a terminal, `javo-env` lists it and `javo-env <name>` opens a shell in it (`javo-env <name> python script.py` runs one command).

An environment with no kernel still installs, JupyterLab starts with its default kernels, and the app page's Started step says `The environment <name> has no Jupyter kernel, so JupyterLab does not offer it. Add ipykernel (or another kernel) to it, then restart the app.`

## Previews and jobs

- A [pull request preview](/using/previews/) runs in its parent's environment. Its home is scratch space, so it installs at every start.
- Jobs install their environment the same way before each run, and reuse an installed one.

## When it fails

- **Environment did not install**, with pixi's own error: the install step failed. Check the lockfile and the platform list.
- **Environment lacks the program**: a picked environment or a repository's lockfile does not have the program the template starts. The evidence names both.
- **Environment not available**: the stored files are missing or too large, or the platform has no pixi image set. A job then waits as Blocked.
