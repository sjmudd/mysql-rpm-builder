############################################################################
#                                                                          #
# rpm build functions for OS9                                              #
#                                                                          #
############################################################################

build () {
    install_srpms $(get_srpm_location)
    rpmbuild_rpms
}