#!/bin/sh

set -e

prepare() {
	cd /data

	yum update -y
	yum install -y 'dnf-command(config-manager)'

#	# Handle OEL7 differences vs CentOS 7
#	if rpm -q oraclelinux-release 2>&1 >/dev/null; then
#		extra_repo=ol7_codeready_builder
#	else
#		extra_repo=powertools
#	fi
#	echo "### Enabling extra repo: $extra_repo"
#	yum config-manager --set-enabled $extra_repo

	yum install -y centos-release-scl

	echo "### Installing required rpms"
	yum install -y \
		bind-utils \
		bison \
		cmake3 \
		cyrus-sasl-devel libaio-devel \
		devtoolset-11-binutils \
		devtoolset-11-gcc \
		devtoolset-11-gcc-c++ \
		git \
		libcurl-devel \
		libtirpc-devel \
		libudev-devel \
		ncurses-devel \
		numactl-devel \
		openldap-devel \
		openssl-devel \
		perl \
		perl-Data-Dumper \
		perl-Env \
		perl-JSON rpcgen \
		rpm-build \
		time \
		wget


#        	gcc-toolset-12-annobin-annocheck \
#	        gcc-toolset-12-annobin-plugin-gcc \

#	# patch gcc-toolset to avoid build problems
#	if ! [ -e /opt/rh/gcc-toolset-11/root/usr/lib/gcc/x86_64-redhat-linux/11/plugin/gcc-annobin.so ]; then
#		echo "### symlinking gcc-annobin.so to annobin.so"
#		(
#			cd /opt/rh/gcc-toolset-11/root/usr/lib/gcc/x86_64-redhat-linux/11/plugin/ && \
#			ln -s annobin.so gcc-annobin.so
#		)
#	else
#		echo "### symlink gcc-annobin.so already exists"
#	fi
#
	# ensure devtoolset-11 is enabled when building
	if ! grep /opt/rh/devtoolset-11/enable /etc/bashrc; then
		echo "### Patching /etc/bashrc to enable devtoolset-11"
		echo "source /opt/rh/devtoolset-11/enable" >> /etc/bashrc
	else
		echo "### /etc/bashrc already patched to enable devtoolset-11"
	fi

	echo "########################################################"
	echo "#           os preparation complete                    #"
	echo "########################################################"
}
