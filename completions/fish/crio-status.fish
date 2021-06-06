# crio-status fish shell completion

function __fish_crio-status_no_subcommand --description 'Test if there has been any subcommand yet'
    for i in (commandline -opc)
        if contains -- $i checkpoint complete completion config c containers container cs s info i man markdown md restore help h
            return 1
        end
    end
    return 0
end

complete -c crio-status -n '__fish_crio-status_no_subcommand' -f -l debug -s d -d 'Enable debug output'
complete -c crio-status -n '__fish_crio-status_no_subcommand' -l socket -s s -r -d 'absolute path to the unix socket'
complete -c crio-status -n '__fish_crio-status_no_subcommand' -f -l timeout -r -d 'Timeout of connecting to server'
complete -c crio-status -n '__fish_crio-status_no_subcommand' -f -l help -s h -d 'show help'
complete -c crio-status -n '__fish_crio-status_no_subcommand' -f -l version -s v -d 'print the version'
complete -c crio-status -n '__fish_crio-status_no_subcommand' -f -l help -s h -d 'show help'
complete -c crio-status -n '__fish_crio-status_no_subcommand' -f -l version -s v -d 'print the version'
complete -c crio-status -n '__fish_seen_subcommand_from checkpoint' -f -l help -s h -d 'show help'
complete -r -c crio-status -n '__fish_crio-status_no_subcommand' -a 'checkpoint' -d 'Checkpoints one or more containers/pods'
complete -c crio-status -n '__fish_seen_subcommand_from checkpoint' -f -l compress -s c -r -d 'Select compression algorithm (gzip, none, zstd) for checkpoint archive.'
complete -c crio-status -n '__fish_seen_subcommand_from checkpoint' -f -l export -s e -r -d 'Specify the name of the tar archive used to export the checkpoint image.'
complete -c crio-status -n '__fish_seen_subcommand_from checkpoint' -f -l keep -s k -d 'Keep all temporary checkpoint files.'
complete -c crio-status -n '__fish_seen_subcommand_from checkpoint' -f -l leave-running -s R -d 'Leave the container running after writing checkpoint to disk.'
complete -c crio-status -n '__fish_seen_subcommand_from checkpoint' -f -l tcp-established -d 'Checkpoint a container with established TCP connections.'
complete -c crio-status -n '__fish_seen_subcommand_from complete completion' -f -l help -s h -d 'show help'
complete -r -c crio-status -n '__fish_crio-status_no_subcommand' -a 'complete completion' -d 'Generate bash, fish or zsh completions.'
complete -c crio-status -n '__fish_seen_subcommand_from complete completion' -f -l help -s h -d 'show help'
complete -c crio-status -n '__fish_seen_subcommand_from config c' -f -l help -s h -d 'show help'
complete -r -c crio-status -n '__fish_crio-status_no_subcommand' -a 'config c' -d 'Show the configuration of CRI-O as TOML string.'
complete -c crio-status -n '__fish_seen_subcommand_from containers container cs s' -f -l help -s h -d 'show help'
complete -r -c crio-status -n '__fish_crio-status_no_subcommand' -a 'containers container cs s' -d 'Display detailed information about the provided container ID.'
complete -c crio-status -n '__fish_seen_subcommand_from containers container cs s' -f -l id -s i -r -d 'the container ID'
complete -c crio-status -n '__fish_seen_subcommand_from info i' -f -l help -s h -d 'show help'
complete -r -c crio-status -n '__fish_crio-status_no_subcommand' -a 'info i' -d 'Retrieve generic information about CRI-O, like the cgroup and storage driver.'
complete -c crio-status -n '__fish_seen_subcommand_from man' -f -l help -s h -d 'show help'
complete -r -c crio-status -n '__fish_crio-status_no_subcommand' -a 'man' -d 'Generate the man page documentation.'
complete -c crio-status -n '__fish_seen_subcommand_from markdown md' -f -l help -s h -d 'show help'
complete -r -c crio-status -n '__fish_crio-status_no_subcommand' -a 'markdown md' -d 'Generate the markdown documentation.'
complete -c crio-status -n '__fish_seen_subcommand_from restore' -f -l help -s h -d 'show help'
complete -r -c crio-status -n '__fish_crio-status_no_subcommand' -a 'restore' -d 'Restore one or more containers/pods'
complete -c crio-status -n '__fish_seen_subcommand_from restore' -f -l change-mounts -r -d 'Change the source of bind mount <old source:new source>.'
complete -c crio-status -n '__fish_seen_subcommand_from restore' -f -l import -s i -r -d 'Restore from exported checkpoint/pod archive.'
complete -c crio-status -n '__fish_seen_subcommand_from restore' -f -l keep -s k -d 'Keep all temporary checkpoint and restore files.'
complete -c crio-status -n '__fish_seen_subcommand_from restore' -f -l pod -s p -r -d 'Specify POD into which the container will be restored. Defaults to previous POD.'
complete -c crio-status -n '__fish_seen_subcommand_from restore' -f -l tcp-established -d 'Restore a container with established TCP connections.'
complete -c crio-status -n '__fish_seen_subcommand_from help h' -f -l help -s h -d 'show help'
complete -r -c crio-status -n '__fish_crio-status_no_subcommand' -a 'help h' -d 'Shows a list of commands or help for one command'
