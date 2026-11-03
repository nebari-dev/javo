# A tour of the terminal

This folder is a copy of the sample in your home, under `samples/terminal`.
Edit anything here; it is yours, and starting the sample again never
overwrites your changes. `cd ~` takes you to your home folder, where your
other files are.

The prompt shows where you are and what changed:

    alice-shell in ~/samples/terminal on main* via agents
    ❯

the app, the folder, the git branch (`*` when a tracked file changed, and
the commits ahead and behind its upstream), and the active environment.
After a command that fails, the arrow turns red and shows the exit status.

Try it:

    ./tour.sh

git, ripgrep (`rg`), fd, less and tmux are installed. Closing the browser
tab ends the shell; run long work in tmux, or as a job.
