import fixturelib


def total():
    """Sum the totals of every row in the store."""
    sum_ = 0
    rows = fixturelib.fetch_all()
    for row in rows:
        sum_ += row["total"]
    return sum_
