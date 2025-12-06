#!/bin/sh

############################################################################
#                                                                          #
# stage1 setup for OS9                                                     #
#                                                                          #
############################################################################

set -e

echo "### $(basename $0): Ensuring system packages are up to date..."

yum update -y
yum install -y 'dnf-command(config-manager)'
