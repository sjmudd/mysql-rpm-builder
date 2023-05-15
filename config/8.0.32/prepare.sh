#!/bin/sh
#
# build environment for to build MySQL
# - build the root part, sourced from build-environment.sh
#

set -e

prepare() {
	echo "########################################################"
	echo "#           preparing OS for building                  #"
	echo "########################################################"
	if ! grep $BUILD_USER /etc/passwd; then
		echo "### Adding missing build user $BUILD_USER"
		useradd --no-create-home -d /data $BUILD_USER
	else
		echo "### required build user $BUILD_USER already present"
	fi

	cd /data
	yum update -y
	yum install -y 'dnf-command(config-manager)'
	yum config-manager --set-enabled powertools
	echo "### installing required rpms"
	yum install -y bind-utils rpm-build gcc-toolset-11 \
		cyrus-sasl-devel libaio-devel libtirpc-devel ncurses-devel \
		numactl-devel openldap-devel openssl-devel perl time \
		perl-JSON rpcgen git gcc-toolset-11-annobin-plugin-gcc \
		gcc-toolset-11-annobin-annocheck libcurl-devel cmake bison
	# patch gcc-toolset to avoid build problems
	if ! [ -e /opt/rh/gcc-toolset-11/root/usr/lib/gcc/x86_64-redhat-linux/11/plugin/gcc-annobin.so ]; then
		echo "### symlinking gcc-annobin.so to annobin.so"
		(
			cd /opt/rh/gcc-toolset-11/root/usr/lib/gcc/x86_64-redhat-linux/11/plugin/ && \
			ln -s annobin.so gcc-annobin.so
		)
	else
		echo "### symlink gcc-annobin.so already exists"
	fi

	# ensure gcc-toolset-11 is enabled when building
	if ! grep /opt/rh/gcc-toolset-11/enable /etc/bashrc; then
		echo "### patching /etc/bashrc to enable gcc-toolset-11"
		echo "source /opt/rh/gcc-toolset-11/enable" >> /etc/bashrc
	else
		echo "### /etc/bashrc already patched to enable gcc-toolset-11"
	fi
	echo "########################################################"
	echo "#           os preparation complete                    #"
	echo "########################################################"
}
