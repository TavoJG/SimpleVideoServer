# UI/UX Roadmap

Status: Finished. All three phases are implemented and verified.

Phase 1 is complete: clearer sidebar grouping, calmer view controls, stronger media row hierarchy, more compact player navigation, and a more legible media details panel.

## Phase 2: Interaction Upgrades

- Completed: keyboard shortcuts for play/pause, previous, next, search focus, favorite, watch later, edit details, and dialog close.
- Completed: dialogs trap focus, restore focus on close, and respond to Escape when no operation is in progress.
- Completed: synchronous scan feedback reports completion time, found, added, updated, and missing files. Missing files remain indexed rather than being deleted. Thumbnail failures appear per item because thumbnails are generated on demand. Last scan feedback lasts for the current app session.
- Completed: specific loading, disabled, and empty states for locked categories, filtered views, failed thumbnails, and missing `VIDEO_ROOT`.
- Completed: route-aware browser titles for categories, subcategories, and media.
- Completed: returning to the main category grid focuses and scrolls to the previously selected category.

## Phase 3: Library Power Tools

- Completed: media selection, select-all for filtered results, and stable selection across sorting.
- Completed: bulk moves, tag add/remove/replace, explicit favorite/watch-later edits, trash confirmation, per-item errors, and retry.
- Completed: navigable tag browser with exact case-insensitive filtering.
- Completed: trash, restore, destination selection, permanent deletion, and accessible-only Empty Trash. Files are retained until explicitly deleted.
- Completed: subcategory rename, move, promotion, and trash with two-level constraints, collision checks, and preservation of unindexed files.
- Completed: optional compact browsing persisted per browser.
