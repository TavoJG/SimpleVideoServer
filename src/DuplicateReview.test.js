import { describe, it, expect, vi } from 'vitest';
import { mount } from '@vue/test-utils';
import DuplicateReview from './DuplicateReview.vue';

const videos = [1, 2, 3].map(id => ({ id, path: `/library/${id}.mp4`, relative_path: `${id}.mp4`, media_type: 'video', stream_url: `/media/${id}` }));
const match = (left, right) => ({ left: `/library/${left}.mp4`, right: `/library/${right}.mp4`, similarity_percent: 98 });
async function setup(matches = [match(1, 2)], api = vi.fn().mockResolvedValue({ deleted: true })) {
  const wrapper = mount(DuplicateReview, { props: { videos, api } });
  await wrapper.vm.importFile({ target: { files: [{ size: 100, text: async () => JSON.stringify({ matches }) }] } });
  return { wrapper, api };
}
describe('duplicate review', () => {
  it('previews accessible pairs and excludes arbitrary report paths', async () => {
    const { wrapper, api } = await setup([match(1, 2), match(1, 99)]);
    expect(wrapper.findAll('video')).toHaveLength(2);
    expect(wrapper.vm.skipped).toBe(1);
    expect(api).not.toHaveBeenCalled();
    wrapper.unmount();
  });
  it('requires confirmation and preserves a copy in overlapping pairs', async () => {
    const { wrapper, api } = await setup([match(1, 2), match(2, 3)]);
    wrapper.vm.toggle(1, true);
    await wrapper.vm.applyDeletions();
    expect(api).not.toHaveBeenCalled();
    wrapper.vm.toggle(2, true);
    wrapper.vm.confirming = true;
    await wrapper.vm.applyDeletions();
    expect(api).not.toHaveBeenCalled();
    wrapper.vm.toggle(1, false);
    wrapper.vm.confirming = true;
    await wrapper.vm.applyDeletions();
    expect(api).toHaveBeenCalledWith('/api/videos/2/delete', { method: 'POST', body: '{}' });
    expect(wrapper.emitted('deleted')).toEqual([[2]]);
    expect(wrapper.vm.pairs).toHaveLength(0);
    wrapper.unmount();
  });
  it('keeps failed deletions for explicit retry', async () => {
    const { wrapper } = await setup(undefined, vi.fn().mockRejectedValue(new Error('Category locked')));
    wrapper.vm.toggle(1, true);
    wrapper.vm.confirming = true;
    await wrapper.vm.applyDeletions();
    expect(wrapper.vm.selected).toEqual([1]);
    expect(wrapper.vm.message).toContain('Category locked');
    expect(wrapper.emitted('deleted')).toBeUndefined();
    expect(wrapper.vm.confirming).toBe(false);
    wrapper.unmount();
  });
  it('rejects invalid reports and keep both clears selections', async () => {
    const { wrapper } = await setup();
    wrapper.vm.toggle(1, true);
    wrapper.vm.keepBoth();
    expect(wrapper.vm.selected).toEqual([]);
    await wrapper.vm.importFile({ target: { files: [{ size: 10, text: async () => '{"matches":[{}]}' }] } });
    expect(wrapper.vm.message).toBe('Invalid similarity report.');
    expect(wrapper.vm.pairs).toHaveLength(0);
    wrapper.unmount();
  });
});
