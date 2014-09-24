Package dependencies for Push
===

Go versioning support for package management is... suboptimal.

Currently, we are using the stand alone bash script gpm, which
requires a ''Godeps'' file. This file lists the individual packages
imported as well as the pinning change set version.

Unfortunately, the companion script for gpm is not yet able to find
all the dependant libraries, so some manual effort is required.

Getting the list of current dependencies
---
Presuming your at the top of the install path:

    $ GOPATH=`pwd`/.godeps:`pwd`:$GOPATH go list -f '{{join .Deps
"\n"}}' ./...  | xargs go list -f '{{if not
.Standard}}{{.ImportPath}}{{end}}' > deps

will produce a list of currently used libraries for the project in
file `deps`

Once you have the file, you will need to fetch the latest tag or
version for each of the libraries listed.

e.g.

    $ pushd .godeps/src/$LIBRARY
    $ if [ -d .hg ] then; hg log | head -1 |sed "s/changeset:\s*//";fi
    $ if [ -d .git ] then; git log | head -1 | sed "s/commit //";fi

this will produce a tag for the most recent version of the library.

if you are feeling brave, or want to help us debug, you can try using
the ``updateDeps.bash`` script. This works on my system, but there is
little guarantee outside of that at the moment.
