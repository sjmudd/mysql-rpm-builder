#!/bin/sh
#
# build environment for building MySQL from src.rpms
#
BUILD_USER=rpmbuild

set -e

if [ -z "$USER" ]; then
	USER=$(id -un)
fi

build_environment=$1
if [ -z "$build_environment" ]; then
	echo "please provide build_environment name, directory under config"
	exit 1
fi

case "$USER" in
root)
	##########################
	###    run as root     ###
	##########################
	echo "sourcing prepare script"
	. /data/config/$build_environment/prepare.sh
	prepare
	;;
$BUILD_USER)
	##########################
	### run as $BUILD_USER ###
	##########################
	echo "sourcing perform-build script"
	. /data/config/$build_environment/build.sh 
	build
	;;
*)
	echo "unexpected USER $USER, please call the script properly"
	exit 1
esac
