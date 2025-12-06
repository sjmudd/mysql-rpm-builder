#!/bin/sh

############################################################################
#                                                                          #
# repo setup                                                               #
#                                                                          #
############################################################################

set -e

mydir=$(dirname $0)
myname=$(basename $0)

os=$1
mysql_label=$2

config_file=$mydir/${os}__${mysql_label}.repoconf

test -r $config_file || {
    echo "ERROR: $myname: unable to read repo configuration for $os / $mysql_label at $config_file. Please ensure an appropriate file exists"
    exit 1
}
echo "### $myname: os: $os, label: $mysql_label, config_file: $config_file"

echo "- Sourcing repo configuration from: $config_file"
source $config_file

cat <<EOF
=== config ===
- extra_repo:            $extra_repo
- epel_repo:             $epel_repo
- config_manager_enable: $config_manager_enable
==== end =====
EOF

echo "- Applying repo configuration..."
if [ -n "$extra_repo" ]; then
    echo "### Enabling repo: $extra_repo"
    yum config-manager --set-enabled $extra_repo
fi

if [ -n "$epel_repo" ]; then
    echo "### Installing rpm: $epel_repo"
    dnf install -y $epel_repo
fi

if [ -n "$config_manager_enable" ]; then
    echo "### Enabling repo: $config_manager_enable"
    yum config-manager --set-enabled "$config_manager_enable"
fi
