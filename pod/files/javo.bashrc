# javo's shell defaults for interactive bash, for the templates that give a
# terminal (JupyterLab, VS Code, RStudio, the coding agents). It is one of the
# pod files (/opt/javo/javo.bashrc); the pod's /etc/bash.bashrc, which bash
# reads for every interactive shell (login shells through /etc/profile), is
# the image's own plus a line that sources it (internal/podfiles). A user's own
# ~/.bashrc runs after it and wins. No network, no extra programs: the git part
# of the prompt runs only when git is installed.

[[ $- == *i* ]] || return 0

# A login shell's /etc/profile resets PATH: put the pod's pixi environment
# (internal/pixienv) back in front when it is not there.
if [ -r /opt/javo-env/activate.sh ] && [[ ":$PATH:" != *":${CONDA_PREFIX:-/nonexistent}/bin:"* ]]; then
  . /opt/javo-env/activate.sh
fi
# The platform's own commands (javo-api, javo-env, nebi) come last, as
# pod-user.sh puts them.
[[ ":$PATH:" == *":/opt/javo/bin:"* ]] || PATH=$PATH:/opt/javo/bin

# History: large, shared between terminals (appended and re-read at every
# prompt), no repeated or space-prefixed entries.
HISTSIZE=10000
HISTFILESIZE=20000
HISTCONTROL=ignoreboth:erasedups
HISTTIMEFORMAT='%F %T  '
shopt -s histappend cmdhist checkwinsize

# Colours for ls and grep, the usual Debian aliases. Executables (green) and
# links (cyan) get colours that also read on a light background: magenta, and
# underlined in the text colour.
if command -v dircolors >/dev/null; then
  eval "$(dircolors -b)"
  LS_COLORS+=':ex=35:ln=04'
fi
alias ls='ls --color=auto'
alias ll='ls -alF'
alias la='ls -A'
alias l='ls -CF'
alias grep='grep --color=auto'

# less: keep colours, quit when the text fits one screen, smart-case search.
export LESS=-FRi
if [ -z "${PAGER:-}" ] && command -v less >/dev/null; then export PAGER=less; fi

# Tab completion from bash-completion when the image has it; case-insensitive,
# listed on the first Tab, coloured like ls. Up and Down search the history
# for what is already typed.
if ! shopt -oq posix && [ -z "${BASH_COMPLETION_VERSINFO:-}" ] && [ -r /usr/share/bash-completion/bash_completion ]; then
  . /usr/share/bash-completion/bash_completion
fi
if [[ -t 0 ]]; then
  bind 'set completion-ignore-case on'
  bind 'set show-all-if-ambiguous on'
  bind 'set colored-stats on'
  bind 'set mark-symlinked-directories on'
  bind '"\e[A": history-search-backward'
  bind '"\e[B": history-search-forward'
fi

# The prompt, on two lines so the cursor always starts at the same column:
#   alice-report in ~/projects/report on main* ↑1 via analysis
#   ❯
# The host (in a pod, the app's name), the folder (from ~, its last three
# parts), the git branch with "*" when a tracked file changed and the commits
# ahead (↑) and behind (↓) its upstream, and the active environment (a
# venv, else conda's or pixi's). The arrow is green after a command that
# succeeded; after one that failed it is red and its exit status comes first.
# The prompt is rebuilt before each one is shown, so what conda and venv
# activation add to PS1 never stays. Values from git go in through
# variables, never into PS1 itself, so a branch name is never run.
PROMPT_DIRTRIM=3
__javo_has_git=
command -v git >/dev/null && __javo_has_git=1

# Colours, set once. Terminals differ in their palette (JupyterLab's keeps a
# dark-theme palette on its light theme; javo's terminal page follows the
# browser's light or dark mode), so on a 256-colour terminal the prompt uses
# fixed shades that read on white and on black alike (each at least 3.5:1 on
# both). Other terminals get the basic colours, and a dumb one or NO_COLOR
# none. The marks are Unicode only in a UTF-8 locale: bash counts the second
# line's bytes otherwise and puts the cursor in the wrong place.
__javo_prompt_style() {
  local mode=256
  case ${TERM:-dumb} in
    dumb | unknown) mode= ;;
    *256color* | xterm* | screen* | tmux*) ;;
    *) mode=8 ;;
  esac
  [ -z "${NO_COLOR:-}" ] || mode=
  local -A sgr
  case $mode in
    256) sgr=([host]='1' [dim]='38;5;244' [dir]='1;38;5;32' [git]='38;5;134' [dirty]='38;5;166'
      [env]='38;5;31' [ok]='1;38;5;29' [err]='1;38;5;167') ;;
    8) sgr=([host]='1' [dim]='' [dir]='1;34' [git]='35' [dirty]='31' [env]='36' [ok]='32' [err]='1;31') ;;
  esac
  local k
  for k in host dim dir git dirty env ok err; do
    if [ -n "${sgr[$k]:-}" ]; then
      printf -v "__javo_c_$k" '\\[\\e[%sm\\]' "${sgr[$k]}"
    else
      printf -v "__javo_c_$k" ''
    fi
  done
  __javo_c_reset= __javo_title=
  if [ -n "$mode" ]; then
    __javo_c_reset='\[\e[0m\]'
    __javo_title='\[\e]0;\h: \w\a\]'
  fi
  case ${LC_ALL:-${LC_CTYPE:-${LANG:-}}} in
    *[Uu][Tt][Ff]-8* | *[Uu][Tt][Ff]8*) __javo_arrow='❯' __javo_up='↑' __javo_down='↓' ;;
    *) __javo_arrow='>' __javo_up='^' __javo_down='v' ;;
  esac
  [ "${EUID:-1}" != 0 ] || __javo_arrow='#'
}
__javo_prompt_style

# The repository of the current folder, found by reading the folders up to
# the root and the HEAD file: no git process, so the branch costs nothing.
# Then two quick git calls: whether a tracked file changed (git diff, which
# never looks at untracked files, the slow part of git status in big
# repositories) and the commits ahead and behind. JAVO_PROMPT_GIT_STATUS=0
# leaves those out and shows the branch only.
__javo_git_info() {
  __javo_git= __javo_git_dirty= __javo_git_ab=
  [ -n "$__javo_has_git" ] || return 0
  local d=$PWD gitdir=${GIT_DIR:-} head
  while [ -z "$gitdir" ]; do
    if [ -d "$d/.git" ]; then
      gitdir=$d/.git
    elif [ -f "$d/.git" ]; then
      # A worktree or submodule: "gitdir: <path>", maybe relative.
      read -r gitdir <"$d/.git" || true
      gitdir=${gitdir#gitdir: }
      [[ $gitdir == /* ]] || gitdir=$d/$gitdir
    elif [ -z "$d" ]; then
      return 0
    else
      d=${d%/*}
    fi
  done
  read -r head <"$gitdir/HEAD" 2>/dev/null || return 0
  case $head in
    'ref: refs/heads/'*) __javo_git=${head#ref: refs/heads/} ;;
    'ref: '*) __javo_git=${head#ref: } ;;
    *) __javo_git=${head:0:7} ;;
  esac
  [ "${JAVO_PROMPT_GIT_STATUS:-1}" != 0 ] || return 0
  GIT_OPTIONAL_LOCKS=0 git diff --no-ext-diff --quiet --ignore-submodules=dirty HEAD -- 2>/dev/null
  [ $? != 1 ] || __javo_git_dirty='*'
  local behind ahead
  if read -r behind ahead < <(GIT_OPTIONAL_LOCKS=0 git rev-list --count --left-right '@{upstream}...HEAD' 2>/dev/null); then
    [ "${ahead:-0}" = 0 ] || __javo_git_ab+=" $__javo_up$ahead"
    [ "${behind:-0}" = 0 ] || __javo_git_ab+=" $__javo_down$behind"
  fi
}

# The name of the active environment: a venv's prompt name (its folder when
# it has none), else conda's environment (the folder's name for one
# activated by path; pixi's shell sets it too).
__javo_env_info() {
  __javo_env=
  if [ -n "${VIRTUAL_ENV:-}" ]; then
    __javo_env=${VIRTUAL_ENV_PROMPT:-${VIRTUAL_ENV##*/}}
    # Older venvs set the prompt name with its parentheses and a space.
    __javo_env=${__javo_env#(}
    __javo_env=${__javo_env%% }
    __javo_env=${__javo_env%)}
  elif [ -n "${CONDA_DEFAULT_ENV:-}" ]; then
    __javo_env=${CONDA_DEFAULT_ENV##*/}
  fi
}

__javo_prompt() {
  local status=$?
  history -a
  history -n
  __javo_git_info
  __javo_env_info
  local r=$__javo_c_reset dim=$__javo_c_dim
  PS1="$__javo_title$__javo_c_host"'\h'"$r $dim"'in'"$r $__javo_c_dir"'\w'"$r"
  if [ -n "$__javo_git" ]; then
    PS1+=" $dim"'on'"$r $__javo_c_git"'${__javo_git}'"$r$__javo_c_dirty"'${__javo_git_dirty}'"$r$dim"'${__javo_git_ab}'"$r"
  fi
  [ -z "$__javo_env" ] || PS1+=" $dim"'via'"$r $__javo_c_env"'${__javo_env}'"$r"
  PS1+='\n'
  __javo_status=$status
  if [ "$status" -ne 0 ]; then
    PS1+="$__javo_c_err"'${__javo_status} '"$__javo_arrow$r "
  else
    PS1+="$__javo_c_ok$__javo_arrow$r "
  fi
  return "$status"
}

case ";${PROMPT_COMMAND:-};" in
  *";__javo_prompt;"*) ;;
  *) PROMPT_COMMAND="__javo_prompt${PROMPT_COMMAND:+;$PROMPT_COMMAND}" ;;
esac
PS2="$__javo_c_dim... $__javo_c_reset"
