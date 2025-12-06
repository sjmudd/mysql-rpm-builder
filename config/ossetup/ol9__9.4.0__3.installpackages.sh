#!/bin/sh

############################################################################
#                                                                          #
# Package setup for OS9                                                    #
# - take list of rpms from a os / mysql_labels based file                  #
#                                                                          #
############################################################################

set -e

myname=$(basename $0)
mydir=$(dirname $0)

os=$1
mysql_label=$2

packagelist_file=$mydir/${os}__${mysql_label}.rpms

echo "### $myname: os: $os, label: $mysql_label, rpm filelist location: $packagelist_file"

if [ ! -r $packagelist_file ]; then
    echo "ERROR: $myname: packagelist_file $packagelist_file not found"
    exit 1
fi

# generate a single line of package names
packages="$(cat $packagelist_file | tr '\n' ' ')"

echo "### Installing rpms to build MySQL..."
yum install -y $packages

# FIXME: pull this postfix up script out of the rpm installation
gcctoolset_versions="12 13 14"

# FIXME: Handle aarch64 architecture.
# See: https://bugs.mysql.com/bug.php?id=108049. Without this symlink the
# build process will fail to find something the code needs to complete
# the build.
#
# Even better in 9.X we have to support this symlinking in CentOS9 for
# both gcctoolset-12 (8.0/8.4 compat builds) and gcctoolset-13/14 (9.0 builds)
ARCH=$(uname -m) # to handle x86_64 and aarch64
for version in $gcctoolset_versions; do
	PLUGINDIR=/opt/rh/gcc-toolset-$version/root/usr/lib/gcc/$ARCH-redhat-linux/$version/plugin
	if [ -d $PLUGINDIR ]; then
		pushd $PLUGINDIR

		echo "Handling CentOS 9 workaround symlinks for gcc-toolset-$version"
		for p in annobin.so annobin.so.0.0.0 gcc-annobin.so gcc-annobin.so.0.0.0; do
			echo "Symlinking missing $p..."
			test -e $p || ln -s gts-annobin.so.0.0.0 $p
		done
		popd
	fi
done

echo "########################################################"
echo "#           os preparation complete                    #"
echo "########################################################"
