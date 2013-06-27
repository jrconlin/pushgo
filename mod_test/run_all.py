#!/usr/bin/python

import unittest, os
from optparse import OptionParser
from unittest import TestLoader, TextTestRunner, TestSuite

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
