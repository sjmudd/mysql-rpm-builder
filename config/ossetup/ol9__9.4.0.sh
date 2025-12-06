#!/bin/sh

############################################################################
#                                                                          #
# OS Setup functions for OS9                                               #
#                                                                          #
############################################################################

set -e

myname=$(basename $0)
mydir=$(dirname $0)
# remove trailing .sh
cleaned_name=$(echo $myname | sed -e 's/\.sh$//')

# provide OS hints to called scripts
osname=$1
mysql_label=$2

echo "### $myname: called with $osname $mysql_label"

for stage in 1.refresh 2.setuprepos 3.installpackages; do
    # e.g. ol9__9.5.0__3.installpackages.sh
    script=${mydir}/${cleaned_name}__${stage}.sh
    echo "### $myname: running: $script $osname $mysql_label"
    $script $osname $mysql_label
done
