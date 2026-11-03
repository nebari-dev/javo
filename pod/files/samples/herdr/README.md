# Starter project

This folder is a copy of the sample in your home, under `samples/herdr`.
Edit anything here; it is yours, and starting the sample again never
overwrites your changes.

herdr runs your coding agents in panes and shows which one is working,
blocked or done. Claude Code and pi are installed; start one in a pane:

    claude

Each agent signs in with its own flow the first time (pi: type `/login`).
Your sign-in is kept in your home folder, so you do it once.

Then ask it for a change, for example:

    Add a --name option to hello.js and print the greeting for that name.

Split a pane with the mouse (right-click) or with Ctrl-b v, and run the
script yourself in the new one:

    node hello.js

Closing the browser tab keeps herdr and its agents running; open the app
again to come back to them.
