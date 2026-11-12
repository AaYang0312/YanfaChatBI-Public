import type { ReasoningSpec } from '../types/chat';

/** Infer thinking-intensity support from a model ID, mirroring the backend. */
export function inferReasoningSpec(modelId: string): ReasoningSpec | undefined {
    const id = modelId.toLowerCase().trim();
    const effortLevels = ['low', 'medium', 'high', 'xhigh', 'max'];
    if (/(^|[/-])(gpt-6|gpt-5)/.test(id) || /^(o1|o3|o4)/.test(id)) {
        return { mode: 'effort', param: 'reasoning_effort', levels: effortLevels, default_level: 'medium' };
    }
    if (/kimi-k3/.test(id)) {
        return { mode: 'effort', param: 'reasoning_effort', levels: ['low', 'high', 'max'], default_level: 'max' };
    }
    if (/minimax-m3/.test(id)) {
        return { mode: 'custom', param: 'reasoning.effort', levels: effortLevels, default_level: 'max' };
    }
    if (/(glm-4\.7|glm-5)/.test(id)) {
        return { mode: 'toggle', param: 'thinking.type', levels: ['on', 'off'], default_level: 'on', on_value: 'enabled', off_value: 'disabled' };
    }
    if (/qwen3/.test(id)) {
        return { mode: 'toggle', param: 'enable_thinking', levels: ['on', 'off'], default_level: 'on', on_value: true, off_value: false };
    }
    if (/(deepseek-v4|deepseek-reasoner)/.test(id)) {
        return { mode: 'toggle', param: 'thinking.type', levels: ['on', 'off'], default_level: 'on', on_value: true, off_value: false };
    }
    return undefined;
}
