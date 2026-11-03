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

# The prompt, on two lines so the cursor always starts near the same column:
#   (base) alice@alice-report in ~/projects/report on main* ↑1
#   [1] $
# The active Python environment (a venv, else conda's), the user and host
# (in a pod, the app's name), the folder (the last three parts), the git
# branch with "*" when there are changes and the commits ahead (↑) and
# behind (↓) its upstream. The prompt is rebuilt before each one is shown,
# so what conda and venv activation add to PS1 never stays: the environment
# is shown here, once, always in the same place.
# When the last command failed, its exit status starts the second line, in
# red with the "$", where a narrow terminal cannot cut it off. Terminals such as JupyterLab's keep xterm's dark-theme ANSI colours on
# a light background, and draw bold colours in their bright shade, so only
# shades that read on white and on black are used: text-colour bold, bright
# blue, magenta and red. No green, yellow or cyan. Values from git go in
# through variables, never into PS1 itself, so a branch name is never run.
PROMPT_DIRTRIM=3
__javo_has_git=
command -v git >/dev/null && __javo_has_git=1

__javo_git_info() {
  __javo_git=
  [ -n "$__javo_has_git" ] || return 0
  local out line head="" oid="" dirty="" ab=""
  out=$(GIT_OPTIONAL_LOCKS=0 git status --porcelain=v2 --branch 2>/dev/null) || return 0
  while IFS= read -r line; do
    case $line in
      '# branch.head '*) head=${line#\# branch.head } ;;
      '# branch.oid '*) oid=${line#\# branch.oid } ;;
      '# branch.ab '*)
        line=${line#\# branch.ab }
        local ahead=${line%% *} behind=${line##* }
        [ "$ahead" = +0 ] || ab+=" ↑${ahead#+}"
        [ "$behind" = -0 ] || ab+=" ↓${behind#-}"
        ;;
      '#'*) ;;
      ?*) dirty='*' ;;
    esac
  done <<<"$out"
  [ "$head" = "(detached)" ] && head=${oid:0:7}
  __javo_git=$head
  __javo_git_dirty=$dirty
  __javo_git_ab=$ab
}

# The name of the active environment: a venv's prompt name (its folder when
# it has none), else conda's environment (the folder's name for one
# activated by path).
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
  local r='\[\e[0m\]' b='\[\e[1m\]' red='\[\e[31m\]' blue='\[\e[1;34m\]' magenta='\[\e[35m\]'
  local mark=$b
  PS1='\[\e]0;\u: \w\a\]'
  [ -z "$__javo_env" ] || PS1+='(${__javo_env}) '
  PS1+="$b"'\u@\h'"$r in $blue"'\w'"$r"
  if [ -n "$__javo_git" ]; then
    PS1+=" on $magenta"'${__javo_git}'"$r$red"'${__javo_git_dirty}'"$r$magenta"'${__javo_git_ab}'"$r"
  fi
  PS1+='\n'
  if [ "$status" -ne 0 ]; then
    PS1+="$red"'[${__javo_status}] '
    mark=$red
  fi
  __javo_status=$status
  PS1+="$mark"'\$'"$r "
  return "$status"
}

case ";${PROMPT_COMMAND:-};" in
  *";__javo_prompt;"*) ;;
  *) PROMPT_COMMAND="__javo_prompt${PROMPT_COMMAND:+;$PROMPT_COMMAND}" ;;
esac
PS2='\[\e[2m\]... \[\e[0m\]'
