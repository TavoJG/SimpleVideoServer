import { mount, flushPromises } from '@vue/test-utils';
import { describe, it, expect, vi } from 'vitest';
import ClassificationReview from './ClassificationReview.vue';

describe('visual classification review', () => {
  it('requires selection and sends an edited destination on approval', async () => {
    const api = vi.fn(async (path) => path.includes('/apply') ? { results: [{ id: 1, error: '' }] } : {
      enabled: true, jobs: [{ id: 2, status: 'completed', total: 1, processed: 1 }],
      suggestions: [{ id: 1, title: 'Image', subcategory: 'Forests', status: 'pending', thumbnail_url: '/media/1' }],
    });
    const wrapper = mount(ClassificationReview, { props: { category: 'Nature', api } });
    await flushPromises();
    expect(wrapper.find('.save-button').attributes('disabled')).toBeDefined();
    await wrapper.find('input[type="checkbox"]').setValue(true);
    await wrapper.find('input[list]').setValue('Mountains');
    await wrapper.find('.save-button').trigger('click');
    await flushPromises();
    expect(api).toHaveBeenCalledWith('/api/classification/apply', expect.objectContaining({ body: JSON.stringify({ items: [{ id: 1, subcategory: 'Mountains' }] }) }));
    expect(wrapper.emitted('applied')).toHaveLength(1);
    wrapper.unmount();
  });
  it('disables classification when unconfigured', async () => {
    const wrapper = mount(ClassificationReview, { props: { category: 'Nature', api: async () => ({ enabled: false, jobs: [], suggestions: [] }) } });
    await flushPromises();
    expect(wrapper.find('button').attributes('disabled')).toBeDefined();
    wrapper.unmount();
  });
});
