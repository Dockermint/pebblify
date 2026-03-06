package completion

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func GenerateBash() string {
	return `_pebblify() {
    local cur prev commands
    COMPREPLY=()
    cur="${COMP_WORDS[COMP_CWORD]}"
    prev="${COMP_WORDS[COMP_CWORD-1]}"

    commands="level-to-pebble recover verify completion version help"

    if [[ ${COMP_CWORD} -eq 1 ]]; then
        COMPREPLY=( $(compgen -W "${commands}" -- "${cur}") )
        return 0
    fi

    case "${COMP_WORDS[1]}" in
        level-to-pebble)
            local opts="-f --force -w --workers --batch-memory --tmp-dir -v --verbose --health --health-port"
            case "${prev}" in
                --workers|-w|--batch-memory|--health-port)
                    return 0
                    ;;
                --tmp-dir)
                    COMPREPLY=( $(compgen -d -- "${cur}") )
                    return 0
                    ;;
                *)
                    if [[ "${cur}" == -* ]]; then
                        COMPREPLY=( $(compgen -W "${opts}" -- "${cur}") )
                    else
                        COMPREPLY=( $(compgen -d -- "${cur}") )
                    fi
                    return 0
                    ;;
            esac
            ;;
        recover)
            local opts="-w --workers --batch-memory --tmp-dir -v --verbose --health --health-port"
            case "${prev}" in
                --workers|-w|--batch-memory|--health-port)
                    return 0
                    ;;
                --tmp-dir)
                    COMPREPLY=( $(compgen -d -- "${cur}") )
                    return 0
                    ;;
                *)
                    if [[ "${cur}" == -* ]]; then
                        COMPREPLY=( $(compgen -W "${opts}" -- "${cur}") )
                    fi
                    return 0
                    ;;
            esac
            ;;
        verify)
            local opts="-s --sample --stop-on-error -v --verbose"
            case "${prev}" in
                --sample|-s)
                    return 0
                    ;;
                *)
                    if [[ "${cur}" == -* ]]; then
                        COMPREPLY=( $(compgen -W "${opts}" -- "${cur}") )
                    else
                        COMPREPLY=( $(compgen -d -- "${cur}") )
                    fi
                    return 0
                    ;;
            esac
            ;;
        completion)
            if [[ ${COMP_CWORD} -eq 2 ]]; then
                COMPREPLY=( $(compgen -W "bash zsh install" -- "${cur}") )
            elif [[ ${COMP_CWORD} -eq 3 && "${prev}" == "install" ]]; then
                COMPREPLY=( $(compgen -W "bash zsh" -- "${cur}") )
            fi
            return 0
            ;;
    esac
}

complete -F _pebblify pebblify
`
}

func GenerateZsh() string {
	return `#compdef pebblify

_pebblify() {
    local -a commands
    commands=(
        'level-to-pebble:Convert a Tendermint/CometBFT data/ directory from LevelDB to PebbleDB'
        'recover:Resume a previously interrupted conversion'
        'verify:Verify that converted data matches the source'
        'completion:Generate shell completion scripts'
        'version:Show version information'
        'help:Show help'
    )

    _arguments -C \
        '1:command:->command' \
        '*::arg:->args'

    case "${state}" in
        command)
            _describe 'command' commands
            ;;
        args)
            case "${words[1]}" in
                level-to-pebble)
                    _arguments \
                        '(-f --force)'{-f,--force}'[Overwrite existing temporary state]' \
                        '(-w --workers)'{-w,--workers}'[Max concurrent DB conversions]:workers:' \
                        '--batch-memory[Target memory per batch in MB]:memory:' \
                        '--tmp-dir[Directory where .pebblify-tmp will be created]:directory:_directories' \
                        '(-v --verbose)'{-v,--verbose}'[Enable verbose output]' \
                        '--health[Enable HTTP health probe server]' \
                        '--health-port[Port for the health server]:port:' \
                        '1:source directory:_directories' \
                        '2:output directory:_directories'
                    ;;
                recover)
                    _arguments \
                        '(-w --workers)'{-w,--workers}'[Max concurrent DB conversions]:workers:' \
                        '--batch-memory[Target memory per batch in MB]:memory:' \
                        '--tmp-dir[Directory containing .pebblify-tmp]:directory:_directories' \
                        '(-v --verbose)'{-v,--verbose}'[Enable verbose output]' \
                        '--health[Enable HTTP health probe server]' \
                        '--health-port[Port for the health server]:port:'
                    ;;
                verify)
                    _arguments \
                        '(-s --sample)'{-s,--sample}'[Percentage of keys to verify]:percent:' \
                        '--stop-on-error[Stop at first mismatch]' \
                        '(-v --verbose)'{-v,--verbose}'[Show each key being verified]' \
                        '1:source data directory:_directories' \
                        '2:destination data directory:_directories'
                    ;;
                completion)
                    _arguments \
                        '1:action:(bash zsh install)' \
                        '2:shell:(bash zsh)'
                    ;;
            esac
            ;;
    esac
}

_pebblify "$@"
`
}

func InstallBash() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}

	dir := filepath.Join(home, ".bash_completion.d")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("cannot create directory %s: %w", dir, err)
	}

	dest := filepath.Join(dir, "pebblify")
	if err := os.WriteFile(dest, []byte(GenerateBash()), 0o644); err != nil {
		return "", fmt.Errorf("cannot write %s: %w", dest, err)
	}

	return dest, nil
}

func InstallZsh() (string, error) {
	dir := zshCompletionDir()

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("cannot create directory %s: %w", dir, err)
	}

	dest := filepath.Join(dir, "_pebblify")
	if err := os.WriteFile(dest, []byte(GenerateZsh()), 0o644); err != nil {
		return "", fmt.Errorf("cannot write %s: %w", dest, err)
	}

	return dest, nil
}

func zshCompletionDir() string {
	if fpath := os.Getenv("FPATH"); fpath != "" {
		parts := strings.Split(fpath, ":")
		for _, p := range parts {
			if strings.Contains(p, "completions") || strings.Contains(p, "zsh") {
				if info, err := os.Stat(p); err == nil && info.IsDir() {
					return p
				}
			}
		}
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join("/usr/local/share/zsh/site-functions")
	}

	return filepath.Join(home, ".zsh", "completions")
}
