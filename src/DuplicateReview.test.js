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
  it('deletes the chosen copy immediately and removes overlapping pairs', async () => {
    const { wrapper, api } = await setup([match(1, 2), match(2, 3), match(1, 3)]);
    await wrapper.findAll('.delete-button')[1].trigger('click');
    await new Promise(resolve => setTimeout(resolve, 0));
    expect(api).toHaveBeenCalledWith('/api/videos/2/delete', { method: 'POST', body: '{}' });
    expect(wrapper.emitted('deleted')).toEqual([[2]]);
    expect(wrapper.vm.pairs).toHaveLength(1);
    expect(wrapper.vm.pair.left.id).toBe(1);
    expect(wrapper.vm.pair.right.id).toBe(3);
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false);
    wrapper.unmount();
  });
  it('keeps failed deletions visible for retry', async () => {
    const { wrapper } = await setup(undefined, vi.fn().mockRejectedValue(new Error('Category locked')));
    await wrapper.vm.deleteItem(videos[0]);
    expect(wrapper.vm.pairs).toHaveLength(1);
    expect(wrapper.vm.message).toContain('Category locked');
    expect(wrapper.emitted('deleted')).toBeUndefined();
    expect(wrapper.vm.busy).toBe(false);
    wrapper.unmount();
  });
  it('blocks repeated deletion while a request is pending and advances after success', async () => {
    let finish;
    const api = vi.fn(() => new Promise(resolve => { finish = resolve; }));
    const { wrapper } = await setup([match(1, 2), match(2, 3)], api);
    const pending = wrapper.vm.deleteItem(videos[0]);
    await wrapper.vm.deleteItem(videos[1]);
    expect(api).toHaveBeenCalledTimes(1);
    finish({});
    await pending;
    expect(wrapper.vm.pair.left.id).toBe(2);
    await wrapper.vm.deleteItem(videos[0]);
    expect(api).toHaveBeenCalledTimes(1);
    wrapper.unmount();
  });
  it('keeps both without deleting and handles end of report', async () => {
    const { wrapper, api } = await setup([match(1, 2), match(2, 3)]);
    wrapper.vm.keepBoth();
    expect(wrapper.vm.index).toBe(1);
    wrapper.vm.keepBoth();
    expect(wrapper.vm.message).toContain('Both files kept');
    expect(api).not.toHaveBeenCalled();
    wrapper.unmount();
  });
  it('rejects invalid reports', async () => {
    const { wrapper } = await setup();
    await wrapper.vm.importFile({ target: { files: [{ size: 10, text: async () => '{"matches":[{}]}' }] } });
    expect(wrapper.vm.message).toBe('Invalid similarity report.');
    expect(wrapper.vm.pairs).toHaveLength(0);
    wrapper.unmount();
  });
});
