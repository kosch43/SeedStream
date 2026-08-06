# Bundled tracker definitions

These Cardigann tracker definitions come from [Jackett](https://github.com/Jackett/Jackett)
(`src/Jackett.Common/Definitions`), which publishes them under the GNU General
Public License version 2 **or, at your option, any later version**. That "or
later" clause is what makes them redistributable here: SeedStream is GPL-3.0,
and GPL-2.0-only content could not have been bundled.

They are embedded in the binary so the tracker picker is populated out of the
box — choose a tracker and its address is already filled in.

## Keeping them current

Trackers move domains, and a stale definition points at the old one. Definitions
placed in the data directory's `definitions/` folder override the bundled copy
of the same id, so a single tracker can be refreshed without waiting for a
release:

    curl -X POST -H "Authorization: Bearer $ADMIN_TOKEN" \
      --data-binary @sometracker.yml \
      http://your-seedstream/api/trackers/definitions/import

Refreshing the whole set means copying newer files from Jackett's repository
into that folder. Every tracker's URL is also editable in the UI, which is the
quick fix when only the address has changed.
