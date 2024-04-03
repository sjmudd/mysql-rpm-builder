############################################################################
#                                                                          #
# OS Setup functions for OS9                                               #
#                                                                          #
############################################################################

set -e

prepare() {
	cd /data

	yum update -y
	yum install -y 'dnf-command(config-manager)'

	# OEL9 differences vs CentOS 9 stream
	if rpm -q centos-stream-release 2>&1 >/dev/null; then
		echo "- found CentOS 9 stream"
		extra_repo=crb
	elif rpm -q oraclelinux-release 2>&1 >/dev/null; then
		echo "- found Oracle Linux 9"
		extra_repo=ol9_codeready_builder
	else
		echo "- OS not recognised, giving up"
		exit 1
	fi
	echo "### Enabling extra repo: $extra_repo"
	yum config-manager --set-enabled $extra_repo

	echo "### installing required rpms"
	yum install -y \
		bind-utils \
		bison \
		cmake \
		cyrus-sasl-devel \
		git \
		krb5-devel \
		libaio-devel \
		libcurl-devel \
		libfido2-devel \
		libtirpc-devel \
		libudev-devel \
		ncurses-devel \
		numactl-devel \
		openldap-devel \
		openssl-devel \
		perl \
		perl-JSON rpcgen \
		rpm-build \
		time \
		gcc-toolset-12-annobin-annocheck \
		gcc-toolset-12-annobin-plugin-gcc \
		gcc-toolset-12-binutils  \
		gcc-toolset-12-gcc \
		gcc-toolset-12-gcc-c++ \
		wget \
		zlib-devel

	echo "########################################################"
	echo "#           os preparation complete                    #"
	echo "########################################################"
}
