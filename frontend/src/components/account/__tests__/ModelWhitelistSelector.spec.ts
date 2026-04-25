import { describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { mount } from '@vue/test-utils'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key
    })
  }
})

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showInfo: vi.fn()
  })
}))

import ModelWhitelistSelector from '../ModelWhitelistSelector.vue'

const ModelIconStub = defineComponent({
  name: 'ModelIcon',
  props: {
    model: {
      type: String,
      default: ''
    }
  },
  template: '<span>{{ model }}</span>'
})

describe('ModelWhitelistSelector', () => {
  it('fills from explicit backend-provided options instead of frontend defaults', async () => {
    const wrapper = mount(ModelWhitelistSelector, {
      props: {
        modelValue: [],
        platform: 'openai',
        availableModels: ['backend-only-model']
      },
      global: {
        stubs: {
          ModelIcon: ModelIconStub,
          Icon: true
        }
      }
    })

    const fillRelatedButton = wrapper.findAll('button')
      .find(button => button.text() === 'admin.accounts.fillRelatedModels')

    expect(fillRelatedButton).toBeTruthy()
    await fillRelatedButton!.trigger('click')

    expect(wrapper.emitted('update:modelValue')).toEqual([
      [['backend-only-model']]
    ])
  })

  it('keeps selected custom models in the dropdown when explicit options are provided', async () => {
    const wrapper = mount(ModelWhitelistSelector, {
      props: {
        modelValue: ['custom-picked-model'],
        platform: 'openai',
        availableModels: ['backend-only-model']
      },
      global: {
        stubs: {
          ModelIcon: ModelIconStub,
          Icon: true
        }
      }
    })

    await wrapper.get('.cursor-pointer').trigger('click')

    expect(
      wrapper.findAll('button').some(button => button.text().includes('custom-picked-model'))
    ).toBe(true)
  })
})
