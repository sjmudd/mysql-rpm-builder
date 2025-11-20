#!/bin/sh

############################################################################
#                                                                          #
# OS Setup functions for OS8                                               #
#                                                                          #
############################################################################

set -e

cd /data

yum update -y
yum install -y 'dnf-command(config-manager)'

# Handle OEL8 differences vs CentOS 8 stream
if rpm -q oraclelinux-release 2>&1 >/dev/null; then
	extra_repo=ol8_codeready_builder
else
	extra_repo=powertools
fi
echo "### Enabling extra repo: $extra_repo"
yum config-manager --set-enabled $extra_repo

# Install EPEL repo
if rpm -q oraclelinux-release 2>&1 >/dev/null; then
       echo "- setting up oracle-epel-release-el8 repo"
       yum -y install oracle-epel-release-el8
       yum config-manager --set-enabled ol8_developer_EPEL
elif rpm -q almalinux-release 2>&1 >/dev/null; then
       echo "- setting up epel-release repo"
       dnf install -y epel-release
elif rpm -q rocky-release 2>&1 >/dev/null; then
       echo "- setting up epel-release repo"
       dnf install -y epel-release
else
       echo "- EPEL repo handling not supported on this OS yet. Please fix"
       exit 1
fi

echo "### Installing required rpms"
yum install -y \
	bind-utils \
	bison \
	cmake \
	cyrus-sasl-devel libaio-devel \
	gcc-toolset-12-annobin-annocheck \
	gcc-toolset-12-annobin-plugin-gcc \
	gcc-toolset-12-binutils \
	gcc-toolset-12-dwz \
	gcc-toolset-12-gcc \
	gcc-toolset-12-gcc-c++ \
	gcc-toolset-14-annobin-annocheck \
	gcc-toolset-14-annobin-plugin-gcc \
	gcc-toolset-14-binutils  \
	gcc-toolset-14-dwz \
	gcc-toolset-14-gcc \
	gcc-toolset-14-gcc-c++ \
	git \
	libcurl-devel \
	libtirpc-devel \
	libudev-devel \
	ncurses-devel \
	numactl-devel \
	openldap-devel \
	openssl-devel \
    patchelf \
	perl \
	perl-JSON rpcgen \
	rpm-build \
	time \
	wget

# patch gcc-toolset to avoid build problems
if ! [ -e /opt/rh/gcc-toolset-12/root/usr/lib/gcc/x86_64-redhat-linux/12/plugin/gcc-annobin.so ]; then
	echo "### Symlinking gcc-annobin.so to annobin.so"
	(
		cd /opt/rh/gcc-toolset-12/root/usr/lib/gcc/x86_64-redhat-linux/12/plugin/ && \
		ln -s annobin.so gcc-annobin.so
	)
else
	echo "### Symlink gcc-annobin.so already exists"
fi

# ensure gcc-toolset-12 is enabled when building
if ! grep /opt/rh/gcc-toolset-12/enable /etc/bashrc; then
	echo "### Patching /etc/bashrc to enable gcc-toolset-12"
	echo "source /opt/rh/gcc-toolset-12/enable" >> /etc/bashrc
else
	echo "### /etc/bashrc already patched to enable gcc-toolset-12"
fi

echo "########################################################"
echo "#           os preparation complete                    #"
echo "########################################################"
