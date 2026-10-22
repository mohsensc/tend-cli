"""Reads rows from the fixture store."""

import warnings

_ROWS = [{"id": 1, "total": 250}, {"id": 2, "total": 100}]


def iter_rows():
    """Yield the rows one at a time."""
    yield from _ROWS


def fetch_all():
    """Return every row at once. Deprecated: use iter_rows()."""
    warnings.warn(
        "fetch_all() is deprecated and goes away in fixturelib 2.0; use iter_rows()",
        DeprecationWarning,
        stacklevel=2,
    )
    return list(iter_rows())
