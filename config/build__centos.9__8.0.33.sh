#!/bin/sh

build () {
    install_srpms $(get_srpm_location)
    rpmbuild_rpms
}
