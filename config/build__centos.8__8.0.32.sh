#!/bin/sh

build () {
    SRPMS="https://yum.oracle.com/repo/OracleLinux/OL8/MySQL80/community/x86_64/getPackageSource/mysql-community-8.0.32-1.el8.src.rpm"
    install_srpms "$SRPMS"
    rpmbuild_rpms
}
