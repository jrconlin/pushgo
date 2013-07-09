#!/usr/bin/python

# This Source Code Form is subject to the terms of the Mozilla Public
# License, v. 2.0. If a copy of the MPL was not distributed with this
# file, You can obtain one at http://mozilla.org/MPL/2.0/.

import unittest
import os
from optparse import OptionParser
from unittest import TestSuite


if __name__ == '__main__':
    usage = "usage: %prog [file_name.py]"
    parser = OptionParser(usage=usage)
    options, args = parser.parse_args()

    os.chdir('tests')
    suite = TestSuite()
    pattern = args[0] if args else 'test_*.py'
    if "/" in pattern:
        pattern = pattern.split("/")[1]
    tests = unittest.defaultTestLoader.discover('.', pattern=pattern)
    suite.addTests(tests)
    unittest.TextTestRunner(verbosity=2).run(suite)
