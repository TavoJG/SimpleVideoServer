<template>
  <main class="app-shell" :class="{ 'compact-mode': compactMode }">
    <div v-if="bannerMessage" class="status-banner" :class="bannerType">
      {{ bannerMessage }}
    </div>

    <div v-if="!authChecked" class="auth-screen">
      <p class="message">Loading...</p>
    </div>

    <form v-else-if="!authenticated" class="auth-screen" @submit.prevent="login">
      <div class="auth-panel">
        <h1>Video Library</h1>
        <label for="password">Password</label>
        <input id="password" v-model="password" type="password" autocomplete="current-password" autofocus />
        <button class="scan-button" type="submit" :disabled="loggingIn">
          {{ loggingIn ? "Signing in" : "Sign in" }}
        </button>
        <p v-if="authMessage" class="message">{{ authMessage }}</p>
      </div>
    </form>

    <template v-else>
      <aside class="sidebar">
        <section class="sidebar-section library-overview" aria-label="Library overview">
          <header class="brand">
            <h1>Video Library</h1>
            <p>{{ videos.length }} indexed media files</p>
            <div v-if="storageUsage" class="storage-badge" :title="storageUsageTitle">
              <span>Storage</span>
              <strong>{{ storageUsagePercent }}% used</strong>
              <small>{{ formatBytes(storageUsage.used_bytes) }} / {{ formatBytes(storageUsage.total_bytes) }}</small>
            </div>
          </header>

          <form class="scan-form" @submit.prevent="scanFolder">
            <button class="scan-button" type="submit" :disabled="scanning || !configuredRoot" :title="!configuredRoot ? 'VIDEO_ROOT is not configured' : scanning ? 'Scan in progress' : 'Scan library'">
              {{ scanning ? "Scanning" : "Scan library" }}
            </button>
            <p v-if="message" class="message">{{ message }}</p>
            <p v-if="!configuredRoot" class="message">VIDEO_ROOT is not configured. Set a library folder on the server.</p>
            <p v-for="warning in scanResult?.cleanup_warnings || []" :key="warning" class="message" role="status">Cleanup warning: {{ warning }}</p>
            <p v-if="scanResult" class="message" role="status">Last scan: {{ new Date(scanResult.last_scan_at).toLocaleString() }}. Found {{ scanResult.found }}; added {{ scanResult.added }}; updated {{ scanResult.updated }}; missing {{ scanResult.missing || 0 }}. Removed {{ scanResult.removed_folders || 0 }} empty folders and {{ scanResult.removed_categories || 0 }} categories.</p>
          </form>
        </section>

        <section class="sidebar-section library-tools" aria-label="Library tools">
          <nav class="power-navigation" aria-label="Library destinations">
            <button class="secondary-button" @click="showCategories">Categories</button>
            <button class="secondary-button" @click="$router.push({ name: 'tags' })">Tags</button>
            <label class="toggle-control"><input v-model="compactMode" type="checkbox" />Compact</label>
          </nav>
          <div v-if="showCategoryGrid" class="bulk-toolbar" aria-label="Library playback">
            <button class="secondary-button shuffle-all" type="button" :disabled="loadingLibrary || !accessibleLibraryVideos.length" aria-describedby="shuffle-all-help" @click="shuffleAllVideos">Shuffle all videos</button>
            <span id="shuffle-all-help" class="message">{{ accessibleLibraryVideos.length ? 'Play videos across unlocked categories.' : 'No unlocked videos available.' }}</span>
          </div>
          <details v-if="showCategoryGrid" class="category-tag-filters tag-filters">
            <summary>Filter categories by tags <span v-if="activeCategoryTags.length">({{ activeCategoryTags.length }})</span></summary>
            <label for="category-tag-match">Matching</label>
            <select id="category-tag-match" :value="categoryTagMatch" @change="setCategoryTagFilters(activeCategoryTags, $event.target.value)"><option value="all">Match all</option><option value="any">Match any</option></select>
            <label v-for="tag in categoryTagSummaries" :key="tag.name" class="toggle-control"><input type="checkbox" :checked="activeCategoryTags.some(selected => selected.toLowerCase() === tag.name.toLowerCase())" @change="toggleCategoryTag(tag.name)" />{{ tag.name }} ({{ tag.count }} categories)</label>
            <p v-if="!categoryTagSummaries.length">No category tags in the accessible library.</p>
          </details>
          <div v-if="showCategoryGrid && activeCategoryTags.length" class="active-tags" aria-label="Active category tag filters">
            <button v-for="tag in activeCategoryTags" :key="tag.toLowerCase()" class="secondary-button" @click="removeCategoryTag(tag)">{{ tag }} ×</button>
            <button class="secondary-button" @click="setCategoryTagFilters([], 'all')">Clear all</button>
          </div>
          <details v-if="!showCategoryGrid" class="tag-filters">
            <summary>Filter by tags <span v-if="activeTags.length">({{ activeTags.length }})</span></summary>
            <label for="tag-match">Matching</label>
            <select id="tag-match" :value="tagMatch" @change="setTagFilters(activeTags, $event.target.value)"><option value="all">Match all</option><option value="any">Match any</option></select>
            <label v-for="tag in tagSummaries" :key="tag.name" class="toggle-control"><input type="checkbox" :checked="activeTags.some(selected => selected.toLowerCase() === tag.name.toLowerCase())" @change="toggleTag(tag.name)" />{{ tag.name }} ({{ tag.count }})</label>
            <p v-if="!tagSummaries.length">No tags in the accessible library.</p>
          </details>
          <div v-if="!showCategoryGrid && activeTags.length" class="active-tags" aria-label="Active tag filters">
            <button v-for="tag in activeTags" :key="tag.toLowerCase()" class="secondary-button" @click="removeTag(tag)">{{ tag }} ×</button>
            <button class="secondary-button" @click="setTagFilters([], 'all')">Clear all</button>
          </div>
          <div class="field-group">
            <label for="search">Search</label>
            <input id="search" v-model="query" placeholder="Title, path, or tag" />
          </div>

          <div v-if="!showCategoryGrid" class="filter-row">
            <div class="filter-control">
              <label for="media-filter">Show</label>
              <select id="media-filter" :value="mediaFilter" @change="setMediaFilter($event.target.value)">
                <option value="all">Images and videos</option>
                <option value="images">Images only</option>
                <option value="videos">Videos only</option>
              </select>
            </div>

            <div class="filter-control">
              <label for="sort-mode">Sort</label>
              <select id="sort-mode" :value="sortMode" @change="setSortMode($event.target.value)">
                <option value="library">Library order</option>
                <option value="title">Title</option>
                <option value="filename">Filename</option>
                <option value="newest">Newest</option>
                <option value="oldest">Oldest</option>
                <option value="size">Size</option>
              </select>
            </div>
          </div>

          <div v-if="!showCategoryGrid && !isTagView" class="quick-views" aria-label="Library views">
            <button type="button" :class="{ active: viewMode === 'category' }" @click="setViewMode('category')">Category</button>
            <button type="button" :class="{ active: viewMode === 'recent_added' }" @click="setViewMode('recent_added')">Added</button>
            <button type="button" :class="{ active: viewMode === 'favorites' }" @click="setViewMode('favorites')">Favorites</button>
            <button type="button" :class="{ active: viewMode === 'watch_later' }" @click="setViewMode('watch_later')">Later</button>
          </div>
        </section>

        <nav v-if="$route.name === 'tags'" class="tag-browser" aria-label="Tags">
          <button v-for="tag in tagSummaries" :key="tag.name" class="secondary-button" @click="$router.push({ name: 'tag', params: { tag: tag.name } })">{{ tag.name }} <strong>{{ tag.count }}</strong></button>
          <p v-if="!tagSummaries.length" class="message">No tags in the library.</p>
        </nav>
        <div v-else-if="showCategoryGrid" class="category-menu">
          <nav class="category-grid" aria-label="Categories">
            <div
              v-for="category in filteredCategorySummaries"
              :key="category.name"
              class="category-card"
              :class="{ active: selectedCategory === category.name }"
            >
              <button class="category-tile" type="button" :data-category="category.name" :aria-current="selectedCategory === category.name ? 'true' : undefined" @click="selectCategory(category.name)">
                <span>{{ category.name }}</span>
                <strong>{{ category.count }}</strong>
                <span v-if="!category.locked || category.unlocked" class="category-tag-badges"><span v-for="tag in category.tags || []" :key="tag.toLowerCase()" class="category-tag">{{ tag }}</span></span>
                <span v-if="category.locked" class="lock-indicator">
                  {{ category.unlocked ? "Unlocked" : "Locked" }}
                </span>
              </button>
            </div>
          </nav>
          <p v-if="!filteredCategorySummaries.length" class="message" role="status">No categories match these filters.</p>
        </div>

        <details v-else class="media-browser selected-category-menu" open>
          <summary>
            <span>{{ selectedCategoryLabel }}</span>
            <strong>
              {{ filteredVideos.length }} files
            </strong>
          </summary>
          <div class="selected-category-panel">
            <button class="back-button" type="button" @click="isTagView ? $router.push({ name: 'tags' }) : showCategories()">
              {{ isTagView ? 'Back to tags' : "Back to categories" }}
            </button>
            <div v-if="!isTagView && isSelectedCategoryLocked" class="category-lock-status">
              <p class="message">
                {{ isSelectedCategoryUnlocked ? "Extra password verified for this category." : "This category is locked." }}
              </p>
              <button
                v-if="isSelectedCategoryUnlocked"
                class="secondary-button"
                type="button"
                :disabled="categoryUnlocking"
                @click="relockSelectedCategory"
              >
                Lock again
              </button>
            </div>

            <form v-if="!isTagView && isSelectedCategoryLocked && !isSelectedCategoryUnlocked" class="unlock-form" @submit.prevent="unlockSelectedCategory">
              <div>
                <label for="category-password">Category password</label>
                <input
                  id="category-password"
                  v-model="categoryPassword"
                  type="password"
                  autocomplete="current-password"
                />
              </div>
              <button class="save-button" type="submit" :disabled="categoryUnlocking">
                {{ categoryUnlocking ? "Unlocking" : "Unlock category" }}
              </button>
              <p v-if="categoryUnlockMessage" class="message">{{ categoryUnlockMessage }}</p>
            </form>

            <button v-if="!isTagView && viewMode === 'category' && canViewSelectedCategory" class="secondary-button category-tags-edit" @click="openCategoryTags">Edit category tags</button>
            <div v-if="!isTagView && canViewSelectedCategory" class="category-tag-badges"><span v-for="tag in selectedCategorySummary?.tags || []" :key="tag.toLowerCase()" class="category-tag">{{ tag }}</span></div>
            <div v-if="viewMode === 'category' && canManageSelectedCategory" class="category-management">
              <button
                class="secondary-button"
                type="button"
                :disabled="renamingCategory"
                @click="requestRenameCategory(selectedCategory)"
              >
                Rename category
              </button>
              <button
                class="delete-button"
                type="button"
                :disabled="deletingCategory"
                @click="requestDeleteCategory(selectedCategory)"
              >
                Delete category
              </button>
            </div>

            <div class="bulk-toolbar" aria-label="Playback tools">
              <button class="secondary-button shuffle-toggle" type="button" :aria-pressed="shuffleEnabled" :disabled="!shuffleVideoIds.length" aria-describedby="shuffle-help" @click="toggleShuffle">Shuffle videos</button>
              <span id="shuffle-help" class="message">{{ shuffleVideoIds.length ? (shuffleEnabled ? 'Shuffle on · videos only' : 'Shuffle off') : 'No videos match this view.' }}</span>
            </div>
            <div class="bulk-toolbar" aria-label="Selection tools">
              <label class="toggle-control"><input type="checkbox" :checked="allVisibleSelected" :indeterminate="selectedIds.length > 0 && !allVisibleSelected" :disabled="!filteredVideos.length || powerBusy" @change="selectAllVisible($event.target.checked)" />{{ selectedIds.length }} selected</label>
              <button class="secondary-button" :disabled="!selectedIds.length || powerBusy" @click="clearSelection">Clear</button>
              <template v-if="selectedIds.length">
                <button v-for="action in ['move', 'tags', 'favorited', 'watch_later', 'delete']" :key="action" class="secondary-button" :disabled="powerBusy" @click="openBulkDialog(action)">{{ actionLabels[action] }}</button>
              </template>
            </div>
            <div v-if="powerFailures.length" class="failure-panel" role="status">
              <p v-for="failure in powerFailures" :key="failure.id">{{ failure.title || `Media ${failure.id}` }}: {{ failure.error }}</p>
              <button class="secondary-button" :disabled="powerBusy" @click="retryFailures">Retry failed items</button>
              <button class="secondary-button" :disabled="powerBusy" @click="powerFailures = []; retryPayload = null">Dismiss</button>
            </div>
            <div class="video-list" role="list">
              <div
                v-for="video in filteredVideos"
                :key="video.id"
                class="video-row"
                :class="{ active: selected && selected.id === video.id, checked: selectedIds.includes(video.id) }"
                role="listitem"
                @click="selectVideo(video)"
              >
                <input class="row-checkbox" type="checkbox" :aria-label="`Select ${video.title}`" :checked="selectedIds.includes(video.id)" :disabled="powerBusy" @click.stop="selectionShift = $event.shiftKey" @change="toggleSelection(video.id, { target: $event.target, shiftKey: selectionShift })" />
                <button class="media-open" :aria-label="`Open ${video.title}`" @click.stop="selectVideo(video)">
                <span class="thumbnail-frame" aria-hidden="true">
                  <img
                    v-if="video.thumbnail_url && !failedThumbnails[video.id]"
                    class="thumbnail"
                    :src="video.thumbnail_url"
                    alt=""
                    loading="lazy"
                    @error="failedThumbnails[video.id] = true"
                  />
                  <span class="thumbnail-type">{{ video.media_type }}</span>
                </span>
                <span class="video-row-content">
                  <span class="video-title">{{ video.title }}</span>
                  <small v-if="failedThumbnails[video.id]">Thumbnail unavailable</small>
                  <span class="media-badges">
                    <span class="media-type">{{ video.media_type }}</span>
                    <span v-if="video.favorited" class="media-type icon-badge" title="Favorite">★</span>
                    <span v-if="video.watch_later" class="media-type icon-badge" title="Watch Later">◷</span>
                  </span>
                  <span class="video-path">{{ video.relative_path }}</span>
                </span>
                </button>
                <span class="tag-line row-tags"><button v-for="tag in video.tags" :key="tag" @click.stop="$router.push({ name: 'tag', params: { tag } })">{{ tag }}</button></span>
                <span class="row-actions" @click.stop>
                  <button
                    class="icon-button compact"
                    type="button"
                    :title="video.favorited ? 'Remove favorite' : 'Add favorite'"
                    :aria-label="video.favorited ? 'Remove favorite' : 'Add favorite'"
                    @click="toggleVideoFlag(video, 'favorited')"
                  >
                    {{ video.favorited ? "★" : "☆" }}
                  </button>
                  <button
                    class="icon-button compact"
                    type="button"
                    :title="video.watch_later ? 'Remove from Watch Later' : 'Add to Watch Later'"
                    :aria-label="video.watch_later ? 'Remove from Watch Later' : 'Add to Watch Later'"
                    @click="toggleVideoFlag(video, 'watch_later')"
                  >
                    ◷
                  </button>
                </span>
              </div>
              <p v-if="!canViewSelectedCategory" class="message">Unlock this category to view its media.</p>
              <p v-else-if="!filteredVideos.length" class="message">{{ emptyMediaMessage }}</p>
            </div>
          </div>
        </details>
      </aside>

      <section class="viewer">
        <div v-if="selected" class="player-layout">
          <div class="carousel-stage">
            <button
              class="carousel-control previous"
              type="button"
              aria-label="Previous media"
              :disabled="!hasPreviousMedia"
              :title="shuffleEnabled ? (hasPreviousMedia ? 'Previous played video' : 'No previous video in shuffle history') : (hasPreviousMedia ? 'Previous media' : 'First media in this view')"
              @click="playPreviousMedia"
            >
              <span class="carousel-arrow">&lt;</span>
            </button>
            <video
              v-if="selected.media_type === 'video'"
              ref="player"
              class="player"
              :src="selected.stream_url"
              controls
              autoplay
              playsinline
              webkit-playsinline
              @ended="playNextMedia"
            ></video>
            <img
              v-else
              class="image-viewer"
              :src="selected.stream_url"
              :alt="selected.title"
            />
            <button
              class="carousel-control next"
              type="button"
              aria-label="Next media"
              :disabled="!hasNextMedia"
              :title="shuffleEnabled ? (hasNextMedia ? 'Next shuffled video' : 'No other eligible video') : (hasNextMedia ? 'Next media' : 'Last media in this view')"
              @click="playNextMedia"
            >
              <span class="carousel-arrow">&gt;</span>
            </button>
            <div class="carousel-counter">
              {{ selectedPosition }} / {{ filteredVideos.length }}
            </div>
          </div>

          <details ref="mediaDetails" class="details-card" :key="selected.id">
            <summary>
              <span>Media Details</span>
              <small>{{ selected.filename }} · {{ formatBytes(selected.size_bytes) }}</small>
            </summary>
            <form class="details-panel" @submit.prevent="saveSelected">
              <div>
                <label for="title">Title</label>
                <input id="title" v-model="editTitle" />
              </div>
              <div>
                <label for="tags">Tags</label>
                <textarea id="tags" v-model="editTags" placeholder="One tag per line" rows="3"></textarea>
              </div>
              <div>
                <label for="category">Category</label>
                <div class="category-editor">
                  <select v-if="!customCategory" id="category" v-model="editCategory">
                    <option v-for="category in categories" :key="category" :value="category">
                      {{ category }}
                    </option>
                  </select>
                  <input
                    v-else
                    id="category"
                    v-model="editCategory"
                    placeholder="New category"
                    autocomplete="off"
                  />
                  <button
                    v-if="!customCategory"
                    class="icon-button"
                    type="button"
                    title="Create category"
                    aria-label="Create category"
                    @click="enableCustomCategory"
                  >
                    +
                  </button>
                  <button
                    v-else
                    class="icon-button"
                    type="button"
                    title="Use existing category"
                    aria-label="Use existing category"
                    @click="useExistingCategory"
                  >
                    ↩
                  </button>
                </div>
              </div>
              <div class="details-actions">
                <label class="toggle-control">
                  <input v-model="editFavorited" type="checkbox" />
                  Favorite
                </label>
                <label class="toggle-control">
                  <input v-model="editWatchLater" type="checkbox" />
                  Watch Later
                </label>
                <button class="delete-button" type="button" :disabled="deleting" @click="requestDeleteSelected">
                  {{ deleting ? "Deleting" : "Delete" }}
                </button>
                <button class="save-button" type="submit" :disabled="saving">
                  {{ saving ? "Saving" : "Save changes" }}
                </button>
              </div>
            </form>
          </details>
        </div>

        <div v-else class="empty-state">
          <h2>Select media</h2>
          <p>Scan the library, then choose a video or image from the list.</p>
        </div>
      </section>

      <div v-if="categoryTagsDialog" class="dialog-backdrop" role="dialog" aria-modal="true" aria-labelledby="category-tags-title" @click.self="closeCategoryTags">
        <form class="confirm-dialog" @submit.prevent="saveCategoryTags">
          <h2 id="category-tags-title">Edit category tags</h2>
          <p>{{ categoryTagsDialog }}</p>
          <label for="category-tags-input">One tag per line</label>
          <textarea id="category-tags-input" v-model="categoryTagsDraft" rows="4" :disabled="savingCategoryTags"></textarea>
          <p v-if="categoryTagsError" class="message" role="alert">{{ categoryTagsError }}</p>
          <div class="dialog-actions"><button class="secondary-button" type="button" :disabled="savingCategoryTags" @click="closeCategoryTags">Cancel</button><button class="save-button" type="submit" :disabled="savingCategoryTags">{{ savingCategoryTags ? 'Saving...' : 'Save tags' }}</button></div>
        </form>
      </div>
      <div
        v-if="pendingDelete"
        class="dialog-backdrop"
        role="dialog"
        aria-modal="true"
        aria-labelledby="delete-dialog-title"
        @click.self="cancelDelete"
      >
        <div class="confirm-dialog">
          <h2 id="delete-dialog-title">Permanently delete media?</h2>
          <p>This cannot be undone.</p>
          <p>
            Permanently delete:
            <strong>{{ pendingDelete.title || pendingDelete.filename }}</strong>.
          </p>
          <div class="dialog-actions">
            <button class="secondary-button" type="button" :disabled="deleting" @click="cancelDelete">
              Cancel
            </button>
            <button class="delete-button" type="button" :disabled="deleting" @click="confirmDelete">
              {{ deleting ? "Deleting" : "Delete" }}
            </button>
          </div>
        </div>
      </div>

      <div
        v-if="pendingRenameCategory"
        class="dialog-backdrop"
        role="dialog"
        aria-modal="true"
        aria-labelledby="rename-dialog-title"
        @click.self="cancelRenameCategory"
      >
        <form class="confirm-dialog" @submit.prevent="confirmRenameCategory">
          <h2 id="rename-dialog-title">Rename category</h2>
          <p>
            Rename <strong>{{ pendingRenameCategory }}</strong> and its folder.
          </p>
          <div>
            <label for="rename-category">New category name</label>
            <input id="rename-category" v-model="renameCategoryName" autocomplete="off" />
          </div>
          <div class="dialog-actions">
            <button
              class="secondary-button"
              type="button"
              :disabled="renamingCategory"
              @click="cancelRenameCategory"
            >
              Cancel
            </button>
            <button class="save-button" type="submit" :disabled="renamingCategory">
              {{ renamingCategory ? "Renaming" : "Rename" }}
            </button>
          </div>
        </form>
      </div>

      <div
        v-if="pendingDeleteCategory"
        class="dialog-backdrop"
        role="dialog"
        aria-modal="true"
        aria-labelledby="delete-category-dialog-title"
        @click.self="cancelDeleteCategory"
      >
        <div class="confirm-dialog">
          <h2 id="delete-category-dialog-title">Delete category?</h2>
          <p>
            Permanently delete indexed media files in
            <strong>{{ pendingDeleteCategory }}</strong>. This cannot be undone.
          </p>
          <div v-if="powerFailures.length" class="failure-panel" role="status">
            <p v-for="failure in powerFailures" :key="failure.id">{{ failure.title || `Media ${failure.id}` }}: {{ failure.error }}</p>
            <button class="secondary-button" :disabled="powerBusy" @click="retryFailures">Retry failed items</button>
          </div>
          <div class="dialog-actions">
            <button
              class="secondary-button"
              type="button"
              :disabled="deletingCategory || powerBusy"
              @click="cancelDeleteCategory"
            >
              Cancel
            </button>
            <button class="delete-button" type="button" :disabled="deletingCategory || powerBusy || powerFailures.length > 0" @click="confirmDeleteCategory">
              {{ deletingCategory ? "Deleting" : "Delete category" }}
            </button>
          </div>
        </div>
      </div>
      <div v-if="powerDialog" class="dialog-backdrop" role="dialog" aria-modal="true" aria-labelledby="power-dialog-title" @click.self="closePowerDialog">
        <form class="confirm-dialog" @submit.prevent="submitPowerDialog">
          <h2 id="power-dialog-title">{{ actionLabels[powerDialog.action] }}</h2>
          <p>{{ powerDialog.ids.length }} media files</p>
          <p v-if="powerDialog.action === 'delete'">These files will be permanently deleted. This cannot be undone.</p>
          <template v-if="powerDialog.action === 'move'">
              <label for="power-category">Destination category</label>
              <input id="power-category" v-model="powerDialog.targetCategory" list="power-categories" required autocomplete="off" />
              <datalist id="power-categories"><option v-for="category in availableDestinations" :key="category" :value="category" /></datalist>
          </template>
          <template v-if="powerDialog.action === 'tags'">
            <label for="power-tags">Tags</label><textarea id="power-tags" v-model="powerDialog.tags" placeholder="One tag per line" rows="3"></textarea>
            <label for="power-tag-mode">Tag operation</label><select id="power-tag-mode" v-model="powerDialog.tagMode"><option value="add">Add</option><option value="remove">Remove</option><option value="replace">Replace</option></select>
          </template>
          <label v-if="['favorited', 'watch_later'].includes(powerDialog.action)" class="toggle-control"><input v-model="powerDialog.value" type="checkbox" />{{ actionLabels[powerDialog.action] }}</label>
          <p v-if="powerError" class="message" role="alert">{{ powerError }}</p>
          <div v-if="powerDialog.retry && powerFailures.length" class="failure-panel" role="status">
            <p v-for="failure in powerFailures" :key="failure.id">{{ failure.title || `Media ${failure.id}` }}: {{ failure.error }}</p>
          </div>
          <div class="dialog-actions"><button class="secondary-button" type="button" :disabled="powerBusy" @click="closePowerDialog">Cancel</button><button class="save-button" type="submit" :disabled="powerBusy">{{ powerBusy ? 'Working...' : 'Confirm' }}</button></div>
        </form>
      </div>
    </template>
  </main>
</template>

<script>
import { createShuffle, nextShuffle } from "./shuffle";
export default {
  data() {
    return {
      shuffleEnabled: (() => { try { return localStorage.getItem('video-library-shuffle') === 'true'; } catch { return false; } })(),
      shuffleState: createShuffle([], null),
      compactMode: (() => { try { return localStorage.getItem('video-library-compact') === 'true'; } catch { return false; } })(),
      selectedIds: [],
      selectionAnchor: null,
      selectionShift: false,
      powerDialog: null,
      powerBusy: false,
      powerError: '',
      powerFailures: [],
      retryPayload: null,
      actionLabels: { move: 'Move', tags: 'Edit tags', favorited: 'Favorite', watch_later: 'Watch Later', rename: 'Rename', delete: 'Delete permanently' },
      categorySummariesData: [],
      categoryTagsDialog: null,
      categoryTagsDraft: '',
      categoryTagsError: '',
      savingCategoryTags: false,
      videos: [],
      categoryMappings: [],
      mappingsLoaded: false,
      selected: null,
      editTitle: "",
      editTags: "",
      editCategory: "",
      editFavorited: false,
      editWatchLater: false,
      customCategory: false,
      configuredRoot: "",
      storageUsage: null,
      authChecked: false,
      authenticated: false,
      authEnabled: false,
      password: "",
      authMessage: "",
      loggingIn: false,
      query: "",
      mediaFilter: "all",
      sortMode: "library",
      viewMode: "category",
      selectedCategory: "Uncategorized",
      message: "",
      scanning: false,
      scanResult: null,
      loadingLibrary: false,
      failedThumbnails: {},
      saving: false,
      deleting: false,
      bannerMessage: "",
      bannerType: "success",
      bannerTimer: null,
      pendingDelete: null,
      pendingRenameCategory: null,
      renameCategoryName: "",
      renamingCategory: false,
      pendingDeleteCategory: null,
      deletingCategory: false,
      categoryPassword: "",
      categoryUnlocking: false,
      categoryUnlockMessage: "",
    };
  },
  computed: {
    isTagView() { return ['tags', 'tag', 'tag-media', 'browse', 'browse-media'].includes(this.$route.name); },
    activeTags() {
      const query = this.$route.query.tag;
      const values = [...(this.$route.params.tag ? [String(this.$route.params.tag)] : []), ...(Array.isArray(query) ? query : query ? [query] : [])];
      const seen = new Set();
      return values.map(String).map(tag => tag.trim()).filter(tag => tag && !seen.has(tag.toLowerCase()) && seen.add(tag.toLowerCase()));
    },
    tagMatch() { return this.$route.query.match === 'any' ? 'any' : 'all'; },
    tagSummaries() {
      const counts = new Map();
      for (const video of this.videos) {
        if (!this.canAccessVideo(video)) continue;
        const seen = new Set();
        for (const tag of video.tags || []) {
          const key = tag.toLowerCase();
          if (seen.has(key)) continue;
          seen.add(key);
          const entry = counts.get(key) || { name: tag, count: 0 };
          entry.count++;
          counts.set(key, entry);
        }
      }
      return [...counts.values()].sort((a, b) => a.name.localeCompare(b.name));
    },
    allVisibleSelected() { return this.filteredVideos.length > 0 && this.filteredVideos.every(video => this.selectedIds.includes(video.id)); },
    selectionContext() {
      return JSON.stringify([this.activeTags, this.tagMatch, this.$route.params.category, this.$route.params.tag, this.isTagView, this.showCategoryGrid, this.viewMode, this.mediaFilter, this.query]);
    },
    availableDestinations() { return this.categorySummaries.filter(category => !category.locked || category.unlocked).map(category => category.name); },
    activeDialog() {
      return this.categoryTagsDialog || this.powerDialog || this.pendingDelete || this.pendingRenameCategory || this.pendingDeleteCategory;
    },
    browserTitle() {
      if (!this.authenticated || this.showCategoryGrid) return "Video Library";
      const label = this.canViewSelectedCategory && this.selected && !this.selected.missing
        ? this.selected.title : this.selectedCategoryLabel;
      return `${label} - Video Library`;
    },
    emptyMediaMessage() {
      if (this.loadingLibrary) return "Loading media...";
      if (!this.configuredRoot) return "No library folder is configured.";
      if (this.isTagView) return "No media matches these tags.";
      if (this.query.trim() || this.mediaFilter !== "all") return "No media matches these filters.";
      if (this.viewMode !== "category") return `No media in ${this.selectedCategoryLabel}.`;
      return "This category has no media.";
    },
    showCategoryGrid() {
      return this.$route.name === "categories";
    },
    filteredVideos() {
      const query = this.query.trim().toLowerCase();
      const filtered = this.videos.filter((video) => {
        if (!this.canAccessVideo(video)) return false;
        if (this.activeTags.length) {
          const available = new Set((video.tags || []).map(tag => tag.toLowerCase()));
          const matches = this.activeTags.map(tag => available.has(tag.toLowerCase()));
          if (this.tagMatch === 'any' ? !matches.some(Boolean) : !matches.every(Boolean)) return false;
        }
        const category = video.category || "Uncategorized";
        if (this.viewMode === "category" && !this.isTagView) {
          if (category !== this.selectedCategory) return false;
        } else if (this.viewMode === "favorites") {
          if (!video.favorited) return false;
        } else if (this.viewMode === "watch_later") {
          if (!video.watch_later) return false;
        } else if (this.viewMode === "recent_added") {
          if (!video.created_at) return false;
        }
        if (this.mediaFilter === "images" && video.media_type !== "image") return false;
        if (this.mediaFilter === "videos" && video.media_type !== "video") return false;
        if (!query) return true;
        const haystack = [
          video.title,
          video.filename,
          video.relative_path,
          video.media_type,
          category,
          video.tags.join(" "),
        ]
          .join(" ")
          .toLowerCase();
        return haystack.includes(query);
      });
      return this.sortVideos(filtered);
    },
    activeCategoryTags() {
      const query = this.$route.query.category_tag;
      const values = Array.isArray(query) ? query : query ? [query] : [];
      const seen = new Set();
      return values.map(String).map(tag => tag.trim()).filter(tag => tag && !seen.has(tag.toLowerCase()) && seen.add(tag.toLowerCase()));
    },
    categoryTagMatch() { return this.$route.query.category_match === 'any' ? 'any' : 'all'; },
    categoryTagSummaries() {
      const counts = new Map();
      for (const category of this.categorySummaries) {
        if (category.locked && !category.unlocked) continue;
        const seen = new Set();
        for (const tag of category.tags || []) {
          const key = tag.toLowerCase();
          if (seen.has(key)) continue;
          seen.add(key);
          const entry = counts.get(key) || { name: tag, count: 0 };
          entry.count++; counts.set(key, entry);
        }
      }
      return [...counts.values()].sort((a, b) => a.name.localeCompare(b.name));
    },
    filteredCategorySummaries() {
      const query = this.query.trim().toLowerCase();
      return this.categorySummaries.filter(category => {
        if (query && !category.name.toLowerCase().includes(query)) return false;
        if (!this.activeCategoryTags.length) return true;
        if (category.locked && !category.unlocked) return false;
        const tags = new Set((category.tags || []).map(tag => tag.toLowerCase()));
        const matches = this.activeCategoryTags.map(tag => tags.has(tag.toLowerCase()));
        return this.categoryTagMatch === 'any' ? matches.some(Boolean) : matches.every(Boolean);
      });
    },
    categorySummaries() {
      return this.categorySummariesData;
    },
    categories() {
      return this.categorySummaries.map((category) => category.name);
    },
    selectedCategorySummary() {
      return this.categorySummaries.find((category) => category.name === this.selectedCategory) || null;
    },
    selectedCategoryLabel() {
      const labels = {
        favorites: "Favorites",
        watch_later: "Watch Later",
        recent_added: "Recently Added",
      };
      if (this.isTagView) return this.activeTags.length ? `Tags: ${this.activeTags.join(' · ')}` : 'All media';
      if (this.viewMode !== "category") return labels[this.viewMode] || "Library";
      return this.selectedCategory;
    },
    isSelectedCategoryLocked() {
      if (this.isTagView || this.viewMode !== 'category') return false;
      return Boolean(this.selectedCategorySummary && this.selectedCategorySummary.locked);
    },
    isSelectedCategoryUnlocked() {
      return Boolean(this.selectedCategorySummary && this.selectedCategorySummary.unlocked);
    },
    canViewSelectedCategory() {
      if (this.isTagView || this.viewMode !== 'category') return true;
      return !this.isSelectedCategoryLocked || this.isSelectedCategoryUnlocked;
    },
    canManageSelectedCategory() {
      if (this.isTagView || this.viewMode !== 'category') return false;
      return (
        this.selectedCategory !== "Uncategorized" &&
        this.categories.includes(this.selectedCategory) &&
        this.canViewSelectedCategory
      );
    },
    accessibleLibraryVideos() { return this.videos.filter(video => video.media_type === 'video' && this.canAccessVideo(video)); },
    shuffleVideoIds() { return this.filteredVideos.filter(video => video.media_type === 'video').map(video => video.id); },
    shuffleMembership() { return JSON.stringify([...this.shuffleVideoIds].sort((a, b) => a - b)); },
    selectedIndex() {
      if (!this.selected) return -1;
      return this.filteredVideos.findIndex((video) => video.id === this.selected.id);
    },
    selectedPosition() {
      return this.selectedIndex >= 0 ? this.selectedIndex + 1 : 0;
    },
    hasPreviousMedia() {
      if (this.shuffleEnabled) return this.shuffleState.position > 0;
      return this.selectedIndex > 0;
    },
    hasNextMedia() {
      if (this.shuffleEnabled) return this.shuffleVideoIds.length > 0 && (this.shuffleState.position < this.shuffleState.history.length - 1 || this.shuffleState.queue.length > 0 || this.shuffleVideoIds.some(id => id !== this.selected?.id));
      return this.selectedIndex >= 0 && this.selectedIndex < this.filteredVideos.length - 1;
    },
    storageUsagePercent() {
      if (!this.storageUsage?.total_bytes) return 0;
      return Math.round((this.storageUsage.used_bytes / this.storageUsage.total_bytes) * 100);
    },
    storageUsageTitle() {
      if (!this.storageUsage) return "";
      return `${this.formatBytes(this.storageUsage.available_bytes)} available`;
    },
  },
  watch: {
    compactMode(value) { try { localStorage.setItem('video-library-compact', String(value)); } catch { /* Browsing remains available without storage. */ } },
    selectionContext() { this.clearSelection(); this.resetShuffle(); },
    shuffleMembership() { this.resetShuffle(); },
    filteredVideos() {
      const visible = new Set(this.filteredVideos.map(video => video.id));
      this.selectedIds = this.selectedIds.filter(id => visible.has(id));
      if (!visible.has(this.selectionAnchor)) this.selectionAnchor = null;
    },
    browserTitle: { immediate: true, handler(value) { document.title = value; } },
    activeDialog(value, previous) {
      if (value && !previous) this.dialogOpener = document.activeElement;
      this.$nextTick(() => {
        if (value) {
          const dialog = this.$el.querySelector('[role="dialog"]');
          dialog?.setAttribute("tabindex", "-1");
          (dialog?.querySelector("input, textarea, select, button:not(:disabled)") || dialog)?.focus();
        } else {
          const opener = this.dialogOpener;
          if (opener?.isConnected) opener.focus();
          else this.$el.querySelector("#search")?.focus();
          this.dialogOpener = null;
        }
      });
    },
    $route() {
      this.syncRouteState();
    },
    query() {
      if (this.showCategoryGrid) return;
      this.$nextTick(() => {
        this.ensureSelectedInFilteredList();
      });
    },
    mediaFilter() {
      if (this.showCategoryGrid) return;
      this.$nextTick(() => {
        this.ensureSelectedInFilteredList();
      });
    },
    sortMode() {
      if (this.showCategoryGrid) return;
      this.$nextTick(() => {
        this.ensureSelectedInFilteredList();
      });
    },
    viewMode() {
      if (this.showCategoryGrid) return;
      this.$nextTick(() => {
        this.ensureSelectedInFilteredList();
      });
    },
  },
  async mounted() {
    window.addEventListener("keydown", this.handleKeydown);
    await this.checkAuth();
  },
  beforeUnmount() {
    window.removeEventListener("keydown", this.handleKeydown);
    clearTimeout(this.bannerTimer);
  },
  methods: {
    setCategoryTagFilters(tags, match = this.categoryTagMatch) {
      this.$router.push({ name: 'categories', query: { ...this.$route.query, category_tag: tags.length ? tags : undefined, category_match: tags.length && match === 'any' ? 'any' : undefined } });
    },
    toggleCategoryTag(tag) {
      const exists = this.activeCategoryTags.some(item => item.toLowerCase() === tag.toLowerCase());
      this.setCategoryTagFilters(exists ? this.activeCategoryTags.filter(item => item.toLowerCase() !== tag.toLowerCase()) : [...this.activeCategoryTags, tag]);
    },
    removeCategoryTag(tag) { this.setCategoryTagFilters(this.activeCategoryTags.filter(item => item.toLowerCase() !== tag.toLowerCase())); },
    openCategoryTags() {
      if (this.activeDialog || !this.canViewSelectedCategory || !this.selectedCategorySummary) return;
      this.categoryTagsDialog = this.selectedCategory;
      this.categoryTagsDraft = (this.selectedCategorySummary.tags || []).join('\n');
      this.categoryTagsError = '';
    },
    closeCategoryTags() { if (!this.savingCategoryTags) { this.categoryTagsDialog = null; this.categoryTagsError = ''; } },
    async saveCategoryTags() {
      if (!this.categoryTagsDialog || this.savingCategoryTags) return;
      this.savingCategoryTags = true; this.categoryTagsError = '';
      try {
        const result = await this.api('/api/categories/tags', { method: 'PATCH', body: JSON.stringify({ category: this.categoryTagsDialog, tags: this.parseTagLines(this.categoryTagsDraft) }) });
        const category = this.categorySummariesData.find(item => item.name === result.category);
        if (category) category.tags = result.tags;
        this.categoryTagsDialog = null;
        this.showBanner('Category tags saved.');
      } catch (error) { this.categoryTagsError = error.message; }
      finally { this.savingCategoryTags = false; }
    },

    parseTagLines(value) {
      const seen = new Set();
      return value.split(/\r?\n/).map(tag => tag.trim()).filter(tag => tag && !seen.has(tag.toLowerCase()) && seen.add(tag.toLowerCase()));
    },
    setTagFilters(tags, match = this.tagMatch) {
      const query = { ...this.$route.query, tag: tags.length ? tags : undefined, match: tags.length && match === 'any' ? 'any' : undefined };
      const name = this.isTagView ? 'browse' : this.$route.name === 'categories' ? 'browse' : this.$route.params.category ? 'category' : 'browse';
      this.selected = null;
      this.$router.push({ name, params: name === 'category' ? { category: this.$route.params.category } : {}, query });
    },
    toggleTag(tag) {
      const existing = this.activeTags.find(item => item.toLowerCase() === tag.toLowerCase());
      this.setTagFilters(existing ? this.activeTags.filter(item => item.toLowerCase() !== tag.toLowerCase()) : [...this.activeTags, tag]);
    },
    removeTag(tag) { this.setTagFilters(this.activeTags.filter(item => item.toLowerCase() !== tag.toLowerCase())); },
    async loadCategoryMappings() {
      const mappings = await this.api('/api/category-migrations');
      this.categoryMappings = Array.isArray(mappings) ? mappings : [];
      this.mappingsLoaded = true;
    },

    canAccessVideo(video) {
      const category = this.categorySummaries.find(item => item.name === (video.category || 'Uncategorized'));
      return !category?.locked || category.unlocked;
    },
    clearSelection() { this.selectedIds = []; this.selectionAnchor = null; },
    selectAllVisible(checked) {
      this.selectedIds = checked ? this.filteredVideos.map(video => video.id) : [];
      this.selectionAnchor = null;
    },
    toggleSelection(id, event = {}) {
      if (this.powerBusy) return;
      const ids = this.filteredVideos.map(video => video.id);
      if (!ids.includes(id)) return;
      const checked = event.target?.checked ?? !this.selectedIds.includes(id);
      let affected = [id];
      if (event.shiftKey && ids.includes(this.selectionAnchor)) {
        const bounds = [ids.indexOf(id), ids.indexOf(this.selectionAnchor)].sort((a, b) => a - b);
        affected = ids.slice(bounds[0], bounds[1] + 1);
      }
      const selection = new Set(this.selectedIds);
      affected.forEach(item => checked ? selection.add(item) : selection.delete(item));
      this.selectedIds = [...selection];
      this.selectionAnchor = id;
      this.selectionShift = false;
    },
    openBulkDialog(action) {
      if (this.powerBusy || this.activeDialog) return;
      const ids = [...this.selectedIds];
      if (!ids.length) return;
      this.powerFailures = [];
      this.retryPayload = null;
      this.powerError = '';
      this.powerDialog = { action, ids, targetCategory: this.availableDestinations[0] || 'Uncategorized', tags: '', tagMode: 'add', value: true };
    },
    closePowerDialog() { if (!this.powerBusy) { this.powerDialog = null; this.powerError = ''; } },
    async submitPowerDialog() {
      if (!this.powerDialog || this.powerBusy) return;
      const dialog = this.powerDialog;
      if (dialog.retry) { await this.retryFailures(); return; }
      const category = dialog.targetCategory?.trim();
      if (dialog.action === 'move') {
        if (!category || /[/\\]/.test(category) || ['.', '..'].includes(category)) { this.powerError = 'Enter a single category name.'; return; }
        const target = this.categorySummaries.find(item => item.name === category);
        if (target?.locked && !target.unlocked) { this.powerError = 'Unlock the destination category first.'; return; }
      }
      const payload = { ids: dialog.ids, action: dialog.action };
      if (dialog.action === 'move') payload.category = category;
      if (dialog.action === 'tags') Object.assign(payload, { tags: this.parseTagLines(dialog.tags), tag_mode: dialog.tagMode });
      if (['favorited', 'watch_later'].includes(dialog.action)) payload.value = dialog.value;
      await this.runBulk(payload);
    },
    recordResults(response, ids, payload) {
      const results = Array.isArray(response) ? response : response.results || [];
      const items = this.videos;
      this.powerFailures = ids.flatMap(id => {
        const result = results.find(item => item.id === id);
        if (result?.success) return [];
        return [{ id, title: items.find(item => item.id === id)?.title, error: result?.error || 'No result returned for this item.' }];
      });
      this.retryPayload = this.powerFailures.length ? { ...payload, ids: this.powerFailures.map(item => item.id), action: payload.action } : null;
      this.selectedIds = this.selectedIds.filter(id => this.powerFailures.some(item => item.id === id));
      const succeeded = new Set(results.filter(item => item.success).map(item => item.id));
      if (payload.action === 'delete') this.videos = this.videos.filter(item => !succeeded.has(item.id));
      for (const result of results) {
        if (!result.success || !result.video || payload.action === 'delete') continue;
        const index = this.videos.findIndex(item => item.id === result.id);
        if (index >= 0) this.videos.splice(index, 1, result.video);
        else this.videos.push(result.video);
      }
      return ids.length - this.powerFailures.length;
    },
    async runBulk(payload) {
      if (this.powerBusy) return;
      this.powerBusy = true;
      this.powerError = '';
      try {
        const results = [];
        for (let start = 0; start < payload.ids.length; start += 1000) {
          const ids = payload.ids.slice(start, start + 1000);
          try {
            const batch = await this.api('/api/videos/bulk', { method: 'POST', body: JSON.stringify({ ...payload, ids }) });
            results.push(...(Array.isArray(batch) ? batch : batch.results || []));
          } catch (error) {
            results.push(...ids.map(id => ({ id, success: false, error: error.message })));
            if (!this.authenticated) break;
          }
        }
        const response = { results };
        const count = this.recordResults(response, payload.ids, payload);
        if (!this.powerFailures.length) { this.powerDialog = null; this.pendingDeleteCategory = null; }
        else if (this.powerDialog) this.powerDialog.retry = true;
        this.showBanner(`${count} succeeded; ${this.powerFailures.length} failed.`, this.powerFailures.length ? 'error' : 'success');
        try { await this.loadLibrary(); }
        catch (error) { this.showBanner(`Operation completed; refresh failed: ${error.message}`, 'error'); }
      } catch (error) {
        this.powerError = error.message;
        this.powerFailures = payload.ids.map(id => ({ id, error: error.message }));
        this.retryPayload = { ...payload, action: payload.action };
      } finally { this.powerBusy = false; }
    },
    async retryFailures() {
      if (this.retryPayload) await this.runBulk({ ...this.retryPayload, ids: this.powerFailures.map(item => item.id) });
    },
    handleKeydown(event) {
      if (this.activeDialog) {
        if (event.key === "Escape") {
          event.preventDefault();
          this.closePowerDialog();
          this.cancelDelete();
          this.cancelRenameCategory();
          this.closeCategoryTags();
          this.cancelDeleteCategory();
        } else if (event.key === "Tab") {
          const dialog = this.$el.querySelector('[role="dialog"]');
          const controls = [...dialog.querySelectorAll('*')].filter(el => el.matches('button, input, textarea, select, [tabindex="0"]') && !el.disabled);
          const index = controls.indexOf(document.activeElement);
          if (!controls.length) { event.preventDefault(); dialog.focus(); }
          else if (event.shiftKey && index <= 0) { event.preventDefault(); controls.at(-1).focus(); }
          else if (!event.shiftKey && (index === controls.length - 1 || index < 0)) { event.preventDefault(); controls[0].focus(); }
        }
        return;
      }
      if (!this.authenticated || event.defaultPrevented || event.repeat || event.ctrlKey || event.metaKey || event.altKey) return;
      if (event.target?.closest?.('input, textarea, select, [contenteditable]:not([contenteditable="false"])')) return;
      const key = event.key.toLowerCase();
      if (key === "/") { event.preventDefault(); this.$el.querySelector("#search")?.focus(); return; }
      if (key === "escape") { this.clearSelection(); this.$refs.mediaDetails?.removeAttribute("open"); this.$el.querySelector("#search")?.focus(); return; }
      if (!this.selected) return;
      if (key === "arrowleft") { event.preventDefault(); this.playPreviousMedia(); }
      else if (key === "arrowright") { event.preventDefault(); this.playNextMedia(); }
      else if (key === "f" || key === "w") { event.preventDefault(); this.toggleVideoFlag(this.selected, key === "f" ? "favorited" : "watch_later"); }
      else if (key === "e") { event.preventDefault(); this.$refs.mediaDetails.open = true; this.$nextTick(() => this.$el.querySelector("#title")?.focus()); }
      else if (key === " " && this.$refs.player && !event.target?.closest?.("button, summary, video")) {
        event.preventDefault();
        const player = this.$refs.player;
        if (player.paused) player.play()?.catch(() => this.showBanner("Playback could not start.", "error"));
        else player.pause();
      }
    },
    async api(path, options = {}) {
      const response = await fetch(path, {
        headers: { "Content-Type": "application/json" },
        ...options,
      });
      const data = await response.json().catch(() => ({}));
      if (!response.ok) {
        if (response.status === 401) {
          this.authenticated = false;
          this.clearSelection();
          this.powerFailures = [];
        }
        throw new Error(data.error || "Request failed");
      }
      return data;
    },
    async checkAuth() {
      try {
        const status = await this.api("/api/auth/status");
        this.authEnabled = status.enabled;
        this.authenticated = status.authenticated;
        if (this.authenticated) {
          await this.loadConfig();
          await this.loadLibrary();
        }
      } catch (error) {
        this.authMessage = error.message;
      } finally {
        this.authChecked = true;
      }
    },
    async login() {
      this.loggingIn = true;
      this.authMessage = "";
      try {
        const result = await this.api("/api/auth/login", {
          method: "POST",
          body: JSON.stringify({ password: this.password }),
        });
        this.authenticated = result.authenticated;
        this.password = "";
        await this.loadConfig();
        await this.loadLibrary();
      } catch (error) {
        this.authMessage = error.message;
      } finally {
        this.loggingIn = false;
      }
    },
    async logout() {
      await this.api("/api/auth/logout", { method: "POST", body: JSON.stringify({}) });
      this.authenticated = false;
      this.clearSelection();
      this.powerFailures = [];
      this.retryPayload = null;
      this.categorySummariesData = [];
      this.videos = [];
      this.selected = null;
      this.categoryPassword = "";
      this.categoryUnlockMessage = "";
      this.$router.push({ name: "categories" });
    },
    async loadConfig() {
      const config = await this.api("/api/config");
      this.configuredRoot = config.default_video_root || "";
      this.storageUsage = config.storage || null;
      if (!this.configuredRoot) this.message = "VIDEO_ROOT is not configured.";
    },
    formatBytes(bytes) {
      if (!Number.isFinite(bytes) || bytes < 0) return "0 B";
      const units = ["B", "KB", "MB", "GB", "TB", "PB"];
      let value = bytes;
      let unitIndex = 0;
      while (value >= 1024 && unitIndex < units.length - 1) {
        value /= 1024;
        unitIndex += 1;
      }
      const precision = value >= 100 || unitIndex === 0 ? 0 : 1;
      return `${value.toFixed(precision)} ${units[unitIndex]}`;
    },
    async loadVideos() {
      this.videos = await this.api("/api/videos");
      if (!Array.isArray(this.videos)) this.videos = [];
    },
    async loadCategories() {
      this.categorySummariesData = await this.api("/api/categories");
      if (!Array.isArray(this.categorySummariesData)) this.categorySummariesData = [];
    },
    async loadLibrary() {
      this.loadingLibrary = true;
      try {
        await Promise.all([this.loadCategories(), this.loadVideos(), this.loadCategoryMappings()]);
        this.syncRouteState();
      } finally {
        this.loadingLibrary = false;
      }
    },
    syncRouteState() {
      if (!this.authenticated) return;
      this.mediaFilter = this.normalizeMediaFilter(this.$route.query.type);
      this.sortMode = this.normalizeSortMode(this.$route.query.sort);
      this.viewMode = this.normalizeViewMode(this.$route.query.view);
      if (this.$route.params.subcategory) {
        if (!this.mappingsLoaded) return;
        const mapping = this.categoryMappings.find(item => item.parent === this.$route.params.category && item.subcategory === this.$route.params.subcategory);
        this.$router.replace(mapping ? { name: this.$route.params.id ? 'media' : 'category', params: { category: mapping.category, ...(this.$route.params.id ? { id: this.$route.params.id } : {}) }, query: this.$route.query } : { name: 'categories', query: this.$route.query });
        return;
      }
      if (this.isTagView) {
        const video = ['tag-media', 'browse-media'].includes(this.$route.name) ? this.filteredVideos.find(item => item.id === Number(this.$route.params.id)) : null;
        this.selected = null;
        if (video) this.selectVideo(video, false);
        return;
      }
      if (this.$route.name === 'categories') { this.selected = null; this.categoryPassword = ''; this.categoryUnlockMessage = ''; this.viewMode = 'category'; return; }
      const category = String(this.$route.params.category || 'Uncategorized');
      if (!this.categories.includes(category)) { this.$router.push({ name: 'categories', query: this.$route.query }); return; }
      this.selectedCategory = category;
      if (this.$route.name !== 'media') { this.selected = null; return; }
      const video = this.filteredVideos.find(item => item.id === Number(this.$route.params.id));
      if (video) this.selectVideo(video, false);
      else { this.selected = null; const replacement = this.filteredVideos[0]; if (replacement) this.selectVideo(replacement); }
    },
    normalizeMediaFilter(value) {
      return value === "images" || value === "videos" ? value : "all";
    },
    normalizeSortMode(value) {
      return ["library", "title", "filename", "newest", "oldest", "size"].includes(value) ? value : "library";
    },
    normalizeViewMode(value) {
      return ["category", "favorites", "watch_later", "recent_added"].includes(value) ? value : "category";
    },
    routeQueryWithMediaFilter(filter = this.mediaFilter, sort = this.sortMode, view = this.viewMode) {
      const normalized = this.normalizeMediaFilter(filter);
      const query = { ...this.$route.query };
      if (normalized === "all") {
        delete query.type;
      } else {
        query.type = normalized;
      }
      const normalizedSort = this.normalizeSortMode(sort);
      if (normalizedSort === "library") {
        delete query.sort;
      } else {
        query.sort = normalizedSort;
      }
      const normalizedView = this.normalizeViewMode(view);
      if (normalizedView === "category" || this.$route.name === "categories") {
        delete query.view;
      } else {
        query.view = normalizedView;
      }
      return query;
    },
    setMediaFilter(filter) {
      const normalized = this.normalizeMediaFilter(filter);
      if (normalized === this.mediaFilter && this.$route.query.type === this.routeQueryWithMediaFilter(normalized).type) {
        return;
      }
      this.$router.push({
        name: this.$route.name,
        params: this.$route.params,
        query: this.routeQueryWithMediaFilter(normalized),
      });
    },
    setSortMode(sort) {
      const normalized = this.normalizeSortMode(sort);
      this.$router.push({
        name: this.$route.name,
        params: this.$route.params,
        query: this.routeQueryWithMediaFilter(this.mediaFilter, normalized),
      });
    },
    setViewMode(view) {
      const normalized = this.normalizeViewMode(view);
      this.$router.push({
        name: this.$route.name,
        params: this.$route.params,
        query: this.routeQueryWithMediaFilter(this.mediaFilter, this.sortMode, normalized),
      });
    },
    selectCategory(category) {
      this.$router.push({ name: "category", params: { category }, query: this.routeQueryWithMediaFilter() });
    },
    async showCategories() {
      const category = this.selectedCategory;
      await this.$router.push({ name: "categories", query: this.routeQueryWithMediaFilter() });
      await this.$nextTick();
      const tile = [...this.$el.querySelectorAll('.category-tile')].find(element => element.dataset.category === category);
      tile?.focus({ preventScroll: true });
      tile?.scrollIntoView?.({ block: 'nearest', inline: 'nearest' });
    },
    selectVideo(video, updateRoute = true, shuffleNavigation = false) {
      if (this.shuffleEnabled && !shuffleNavigation && (updateRoute || this.shuffleState.history[this.shuffleState.position] !== video.id)) {
        this.shuffleState = createShuffle(this.shuffleVideoIds, video.id);
      }
      this.selected = video;
      this.selectedCategory = video.category || "Uncategorized";
      this.editTitle = video.title;
      this.editTags = video.tags.join("\n");
      this.editCategory = video.category || "Uncategorized";
      this.editFavorited = Boolean(video.favorited);
      this.editWatchLater = Boolean(video.watch_later);
      this.customCategory = false;
      this.message = "";
      this.categoryUnlockMessage = "";
      if (updateRoute) {
        this.pushMediaRoute(video);
      }
    },
    pushMediaRoute(video) {
      if (this.isTagView) {
        this.$router.push(this.$route.params.tag ? { name: 'tag-media', params: { tag: this.$route.params.tag, id: video.id }, query: this.routeQueryWithMediaFilter() } : { name: 'browse-media', params: { id: video.id }, query: this.routeQueryWithMediaFilter() });
        return;
      }
      const category = video.category || "Uncategorized";
      this.$router.push({
        name: "media",
        params: { category, id: video.id },
        query: this.routeQueryWithMediaFilter(),
      });
    },
    ensureSelectedInFilteredList() {
      if (!this.selected) return;
      if (this.selectedIndex >= 0) return;
      const replacement = this.filteredVideos[0] || null;
      if (replacement) {
        this.selectVideo(replacement);
        return;
      }
      this.selected = null;
      if (this.isTagView) {
        this.$router.replace(this.$route.params.tag ? { name: 'tag', params: { tag: this.$route.params.tag }, query: this.routeQueryWithMediaFilter() } : { name: 'browse', query: this.routeQueryWithMediaFilter() });
        return;
      }
      this.$router.replace({
        name: "category",
        params: { category: this.selectedCategory },
        query: this.routeQueryWithMediaFilter(),
      });
    },
    showBanner(message, type = "success") {
      this.bannerMessage = message;
      this.bannerType = type;
      if (this.bannerTimer) window.clearTimeout(this.bannerTimer);
      this.bannerTimer = window.setTimeout(() => {
        this.bannerMessage = "";
      }, 2600);
    },
    async shuffleAllVideos() {
      if (!this.accessibleLibraryVideos.length) return;
      this.query = '';
      await this.$router.push({ name: 'browse', query: { type: 'videos' } });
      await this.$nextTick();
      this.shuffleEnabled = true;
      try { localStorage.setItem('video-library-shuffle', 'true'); } catch { /* Playback works without storage. */ }
      this.resetShuffle();
      this.playNextMedia();
    },
    resetShuffle() {
      this.shuffleState = createShuffle(this.shuffleVideoIds, this.selected?.id);
    },
    toggleShuffle() {
      this.shuffleEnabled = !this.shuffleEnabled;
      try { localStorage.setItem('video-library-shuffle', String(this.shuffleEnabled)); } catch { /* Playback works without storage. */ }
      this.resetShuffle();
      if (this.shuffleEnabled && this.selected?.media_type !== 'video') this.playNextMedia();
    },
    playNextMedia() {
      if (this.shuffleEnabled) {
        const id = nextShuffle(this.shuffleState, this.shuffleVideoIds, this.selected?.id);
        const video = this.filteredVideos.find(video => video.id === id);
        if (video) this.selectVideo(video, true, true);
        return;
      }
      if (!this.selected) return;
      const next = this.selectedIndex >= 0 ? this.filteredVideos[this.selectedIndex + 1] : null;
      if (next) this.selectVideo(next);
    },
    playPreviousMedia() {
      if (this.shuffleEnabled) {
        if (this.shuffleState.position > 0) {
          const id = this.shuffleState.history[--this.shuffleState.position];
          const video = this.filteredVideos.find(video => video.id === id);
          if (video) this.selectVideo(video, true, true);
        }
        return;
      }
      if (!this.selected) return;
      const previous = this.selectedIndex > 0 ? this.filteredVideos[this.selectedIndex - 1] : null;
      if (previous) this.selectVideo(previous);
    },
    sortVideos(items) {
      const sorted = [...items];
      const textCompare = (a, b, field) => String(a[field] || "").localeCompare(String(b[field] || ""), undefined, { sensitivity: "base" });
      const timeValue = (value) => {
        const time = Date.parse(value || "");
        return Number.isNaN(time) ? 0 : time;
      };
      if (this.sortMode === "title") {
        sorted.sort((a, b) => textCompare(a, b, "title"));
      } else if (this.sortMode === "filename") {
        sorted.sort((a, b) => textCompare(a, b, "filename"));
      } else if (this.sortMode === "newest") {
        sorted.sort((a, b) => timeValue(b.created_at) - timeValue(a.created_at));
      } else if (this.sortMode === "oldest") {
        sorted.sort((a, b) => timeValue(a.created_at) - timeValue(b.created_at));
      } else if (this.sortMode === "size") {
        sorted.sort((a, b) => (b.size_bytes || 0) - (a.size_bytes || 0));
      }
      if (this.viewMode === "recent_added" && this.sortMode === "library") {
        sorted.sort((a, b) => timeValue(b.created_at) - timeValue(a.created_at));
      }
      return sorted;
    },
    async persistVideoMetadata(video, patch) {
      const updated = await this.api(`/api/videos/${video.id}`, {
        method: "PATCH",
        body: JSON.stringify({
          title: video.title,
          tags: video.tags,
          category: video.category || "Uncategorized",
          favorited: video.favorited,
          watch_later: video.watch_later,
          ...patch,
        }),
      });
      const index = this.videos.findIndex((item) => item.id === updated.id);
      if (index >= 0) this.videos.splice(index, 1, updated);
      if (this.selected && this.selected.id === updated.id) {
        this.selectVideo(updated, false);
      }
      await this.loadCategories();
      return updated;
    },
    async toggleVideoFlag(video, field) {
      const original = Boolean(video[field]);
      video[field] = !original;
      try {
        const updated = await this.persistVideoMetadata(video, { [field]: video[field] });
        this.showBanner(updated[field] ? "Saved to library." : "Removed from library view.");
      } catch (error) {
        video[field] = original;
        this.showBanner(error.message, "error");
      }
    },
    hideThumbnail(event) {
      event.target.hidden = true;
    },
    enableCustomCategory() {
      this.customCategory = true;
      this.$nextTick(() => {
        const input = document.getElementById("category");
        if (input) input.focus();
      });
    },
    useExistingCategory() {
      this.customCategory = false;
      if (!this.categories.includes(this.editCategory)) {
        this.editCategory = this.categories[0] || "Uncategorized";
      }
    },
    async unlockSelectedCategory() {
      if (!this.selectedCategory || !this.isSelectedCategoryLocked) return;
      this.categoryUnlocking = true;
      this.categoryUnlockMessage = "";
      this.message = "";
      try {
        await this.api("/api/categories/unlock", {
          method: "POST",
          body: JSON.stringify({
            category: this.selectedCategory,
            password: this.categoryPassword,
          }),
        });
        this.categoryPassword = "";
        await this.loadLibrary();
        this.showBanner("Category unlocked.");
      } catch (error) {
        this.categoryUnlockMessage = error.message;
        this.showBanner(error.message, "error");
      } finally {
        this.categoryUnlocking = false;
      }
    },
    async relockSelectedCategory() {
      if (!this.selectedCategory || !this.isSelectedCategoryLocked) return;
      this.categoryUnlocking = true; this.categoryUnlockMessage = '';
      try {
        await this.api('/api/categories/lock', { method: 'POST', body: JSON.stringify({ category: this.selectedCategory }) });
        await this.loadLibrary(); this.selected = null;
        if (this.$route.name === 'media') this.$router.push({ name: 'category', params: { category: this.selectedCategory }, query: this.$route.query });
        this.showBanner('Category locked.');
      } catch (error) { this.categoryUnlockMessage = error.message; this.showBanner(error.message, 'error'); }
      finally { this.categoryUnlocking = false; }
    },
    requestRenameCategory(category) {
      if (category === "Uncategorized" || this.renamingCategory) return;
      this.pendingRenameCategory = category;
      this.renameCategoryName = category;
      this.$nextTick(() => {
        const input = document.getElementById("rename-category");
        if (input) input.focus();
      });
    },
    cancelRenameCategory() {
      if (this.renamingCategory) return;
      this.pendingRenameCategory = null;
      this.renameCategoryName = "";
    },
    async confirmRenameCategory() {
      if (!this.pendingRenameCategory) return;
      const from = this.pendingRenameCategory;
      const to = this.renameCategoryName.trim();
      this.renamingCategory = true;
      this.message = "";
      try {
        const result = await this.api("/api/categories/rename", {
          method: "POST",
          body: JSON.stringify({ from, to }),
        });
        await this.loadLibrary();
        this.selectedCategory = result.to;
        if (this.selected && (this.selected.category || "Uncategorized") === from) {
          this.selected = null;
        }
        this.$router.push({ name: "category", params: { category: result.to }, query: this.routeQueryWithMediaFilter() });
        this.showBanner("Category renamed.");
      } catch (error) {
        this.message = error.message;
        this.showBanner(error.message, "error");
      } finally {
        this.renamingCategory = false;
        this.pendingRenameCategory = null;
        this.renameCategoryName = "";
      }
    },
    requestDeleteCategory(category) {
      if (category === "Uncategorized" || this.deletingCategory) return;
      this.pendingDeleteCategory = category;
      this.powerFailures = [];
      this.retryPayload = null;
    },
    cancelDeleteCategory() {
      if (this.deletingCategory || this.powerBusy) return;
      this.pendingDeleteCategory = null;
    },
    async confirmDeleteCategory() {
      if (!this.pendingDeleteCategory) return;

      const category = this.pendingDeleteCategory;
      this.deletingCategory = true;
      this.message = "";
      try {
        const result = await this.api("/api/categories/delete", {
          method: "POST",
          body: JSON.stringify({ category }),
        });
        this.recordResults(result, this.videos.filter(video => video.category === category).map(video => video.id), { action: 'delete' });
        await this.loadLibrary();
        this.selected = null;
        if (!this.powerFailures.length) this.selectedCategory = this.categories[0] || "Uncategorized";
        if (!this.powerFailures.length) this.$router.push({ name: "categories", query: this.routeQueryWithMediaFilter() });
        this.showBanner(`Permanently deleted ${result.deleted || 0} items. ${this.powerFailures.length} failed.${result.folder_retained ? ' Folder retained.' : ''}`, this.powerFailures.length ? 'error' : 'success');
      } catch (error) {
        this.message = error.message;
        this.showBanner(error.message, "error");
      } finally {
        this.deletingCategory = false;
        if (!this.powerFailures.length) this.pendingDeleteCategory = null;
      }
    },
    async scanFolder() {
      this.scanning = true;
      this.message = "";
      try {
        const result = await this.api("/api/scan", {
          method: "POST",
          body: JSON.stringify({}),
        });
        this.scanResult = result;
        this.failedThumbnails = {};
        await this.loadLibrary();
      } catch (error) {
        this.message = `Scan failed: ${error.message}`;
      } finally {
        this.scanning = false;
      }
    },
    async saveSelected() {
      if (!this.selected) return;
      const currentCategory = this.selectedCategory;
      const currentList = [...this.filteredVideos];
      const currentIndex = currentList.findIndex(video => video.id === this.selected.id);
      const fallback = currentList[currentIndex + 1] || currentList[currentIndex - 1] || null;
      this.saving = true; this.message = '';
      try {
        const updated = await this.api(`/api/videos/${this.selected.id}`, { method: 'PATCH', body: JSON.stringify({ title: this.editTitle, tags: this.parseTagLines(this.editTags), category: this.editCategory, favorited: this.editFavorited, watch_later: this.editWatchLater }) });
        const index = this.videos.findIndex(video => video.id === updated.id);
        if (index >= 0) this.videos.splice(index, 1, updated);
        await this.loadCategories();
        if ((updated.category || 'Uncategorized') !== currentCategory && !this.isTagView) {
          const next = fallback ? this.videos.find(video => video.id === fallback.id) : null;
          if (next) this.selectVideo(next);
          else { this.selected = null; this.$router.push({ name: 'category', params: { category: currentCategory }, query: this.$route.query }); }
        } else { this.selectVideo(updated); this.ensureSelectedInFilteredList(); }
        this.showBanner('Changes saved.');
      } catch (error) { this.message = error.message; this.showBanner(error.message, 'error'); }
      finally { this.saving = false; }
    },
    requestDeleteSelected() {
      if (!this.selected || this.deleting) return;
      this.pendingDelete = this.selected;
    },
    cancelDelete() {
      if (this.deleting) return;
      this.pendingDelete = null;
    },
    async confirmDelete() {
      if (!this.pendingDelete || this.deleting) return;
      this.deleting = true; this.message = '';
      try {
        const item = this.pendingDelete;
        const currentList = [...this.filteredVideos];
        const index = currentList.findIndex(video => video.id === item.id);
        const fallback = currentList[index + 1] || currentList[index - 1] || null;
        await this.api(`/api/videos/${item.id}/delete`, { method: 'POST', body: JSON.stringify({}) });
        this.videos = this.videos.filter(video => video.id !== item.id);
        await this.loadCategories();
        if (fallback) this.selectVideo(fallback);
        else {
          this.selected = null;
          if (this.isTagView) this.$router.push({ name: 'browse', query: { ...this.$route.query, tag: this.activeTags } });
          else this.$router.push(this.categories.includes(this.selectedCategory) ? { name: 'category', params: { category: this.selectedCategory }, query: this.$route.query } : { name: 'categories', query: this.$route.query });
        }
        this.showBanner('Permanently deleted.');
      } catch (error) { this.message = error.message; this.showBanner(error.message, 'error'); }
      finally { this.deleting = false; this.pendingDelete = null; }
    },
    formatBytes(bytes) {
      if (!bytes) return "0 B";
      const units = ["B", "KB", "MB", "GB", "TB"];
      const index = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1);
      const value = bytes / 1024 ** index;
      return `${value.toFixed(value >= 10 || index === 0 ? 0 : 1)} ${units[index]}`;
    },
    formatDateTime(value) {
      const date = new Date(value);
      if (Number.isNaN(date.getTime())) return value;
      return `Played ${date.toLocaleString()}`;
    },
  },
};
</script>
