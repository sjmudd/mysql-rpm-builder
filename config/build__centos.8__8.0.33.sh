#!/bin/sh

build () {
    SRPMS="https://yum.oracle.com/repo/OracleLinux/OL8/MySQL80/community/x86_64/getPackageSource/mysql-community-8.0.33-1.el8.src.rpm"
    install_srpms "$SRPMS"
    install_custom_patches $mysql_build_version
    rpmbuild_rpms
}
