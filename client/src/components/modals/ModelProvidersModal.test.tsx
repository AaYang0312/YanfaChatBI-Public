import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import ModelProvidersModal from './ModelProvidersModal';
import { inferReasoningSpec } from '../../utils/reasoning';
import modelProviderService from '../../services/modelProviderService';

vi.mock('../../services/modelProviderService', () => ({
    default: {
        list: vi.fn(),
        create: vi.fn(),
        update: vi.fn(),
        remove: vi.fn(),
        probe: vi.fn(),
        refreshModels: vi.fn(),
    },
}));

const mocked = vi.mocked(modelProviderService);

const provider = {
    id: 'p1',
    name: 'DeepSeek',
    slug: 'deepseek',
    base_url: 'https://api.deepseek.com/v1',
    api_key_masked: 'sk-****alue',
    has_api_key: true,
    enabled: true,
    models: [{ id: 'deepseek-v4-flash', display_name: 'DeepSeek V4 Flash', max_completion_tokens: 8192, context_limit: 131072, temperature: 1 }],
};

afterEach(() => cleanup());

describe('ModelProvidersModal', () => {
    beforeEach(() => {
        vi.clearAllMocks();
        mocked.list.mockResolvedValue([]);
    });

    it('shows the empty state and presets', async () => {
        render(<ModelProvidersModal onClose={() => {}} />);
        await waitFor(() => expect(mocked.list).toHaveBeenCalled());
        expect(screen.getByText('尚未配置模型提供商。添加一个开始使用。')).toBeTruthy();
        expect(screen.getByText('OpenAI')).toBeTruthy();
        expect(screen.getByText('DeepSeek')).toBeTruthy();
        expect(screen.getByText('智谱 GLM')).toBeTruthy();
        expect(screen.getByText('Moonshot Kimi')).toBeTruthy();
        expect(screen.getByText('MiniMax')).toBeTruthy();
        expect(screen.getByText('通义千问')).toBeTruthy();
    });

    it('prefills the form from a preset and saves it', async () => {
        mocked.create.mockResolvedValue(provider);
        render(<ModelProvidersModal onClose={() => {}} />);
        await waitFor(() => expect(mocked.list).toHaveBeenCalled());

        fireEvent.click(screen.getByText('DeepSeek'));
        expect((screen.getByDisplayValue('DeepSeek') as HTMLInputElement).value).toBe('DeepSeek');
        expect((screen.getByDisplayValue('https://api.deepseek.com/v1') as HTMLInputElement).value).toBe('https://api.deepseek.com/v1');
        expect(screen.getByDisplayValue('deepseek-v4-flash')).toBeTruthy();

        fireEvent.change(screen.getByPlaceholderText('填写后获取模型列表'), { target: { value: 'sk-secret' } });
        fireEvent.click(screen.getByText('保存'));
        await waitFor(() => expect(mocked.create).toHaveBeenCalled());
        const payload = mocked.create.mock.calls[0][0];
        expect(payload.name).toBe('DeepSeek');
        expect(payload.preset_id).toBe('deepseek');
        expect(payload.api_key).toBe('sk-secret');
        expect(payload.models?.[0].reasoning?.mode).toBe('toggle');
    });

    it('probes models and lets the user pick them', async () => {
        mocked.probe.mockResolvedValue(['deepseek-v4-flash', 'deepseek-v4-pro']);
        mocked.create.mockResolvedValue(provider);
        render(<ModelProvidersModal onClose={() => {}} />);
        await waitFor(() => expect(mocked.list).toHaveBeenCalled());

        fireEvent.click(screen.getByText('自定义'));
        fireEvent.change(screen.getByPlaceholderText('例如 DeepSeek'), { target: { value: 'Local' } });
        fireEvent.change(screen.getByPlaceholderText('https://api.example.com/v1'), { target: { value: 'http://127.0.0.1:8000/v1' } });
        fireEvent.click(screen.getByText('获取模型列表'));

        await waitFor(() => expect(mocked.probe).toHaveBeenCalledWith('http://127.0.0.1:8000/v1', ''));
        expect(await screen.findByText('deepseek-v4-pro')).toBeTruthy();

        // The first probed model is preselected; select the second as well.
        const checkbox = screen.getByText('deepseek-v4-pro').closest('label')!.querySelector('input[type="checkbox"]') as HTMLInputElement;
        fireEvent.click(checkbox);
        fireEvent.click(screen.getByLabelText('该本地端点无需鉴权'));
        fireEvent.click(screen.getByText('保存'));
        await waitFor(() => expect(mocked.create).toHaveBeenCalled());
        const payload = mocked.create.mock.calls[0][0];
        expect(payload.models?.map(model => model.id)).toEqual(['deepseek-v4-flash', 'deepseek-v4-pro']);
        expect(payload.allow_empty_api_key).toBe(true);
    });

    it('lists existing providers and deletes after confirmation', async () => {
        mocked.list.mockResolvedValue([provider]);
        mocked.remove.mockResolvedValue();
        render(<ModelProvidersModal onClose={() => {}} />);

        expect(await screen.findByText('https://api.deepseek.com/v1')).toBeTruthy();
        expect(screen.getByText(/1 个模型/)).toBeTruthy();
        fireEvent.click(screen.getByTitle('删除'));
        expect(screen.getByText(/确定删除/)).toBeTruthy();
        fireEvent.click(screen.getByText('删除'));
        await waitFor(() => expect(mocked.remove).toHaveBeenCalledWith('p1'));
    });

    it('refreshes an existing provider catalogue', async () => {
        mocked.list.mockResolvedValue([provider]);
        mocked.refreshModels.mockResolvedValue(provider);
        render(<ModelProvidersModal onClose={() => {}} />);

        expect(await screen.findByText('https://api.deepseek.com/v1')).toBeTruthy();
        fireEvent.click(screen.getByTitle('刷新模型列表'));
        await waitFor(() => expect(mocked.refreshModels).toHaveBeenCalledWith('p1'));
    });
});

describe('reasoning inference', () => {
    it('infers effort levels for modern reasoning models', () => {
        expect(inferReasoningSpec('gpt-6-astra')?.levels).toEqual(['low', 'medium', 'high', 'xhigh', 'max']);
        expect(inferReasoningSpec('gpt-5.6-sol')?.mode).toBe('effort');
        expect(inferReasoningSpec('kimi-k3')?.levels).toEqual(['low', 'high', 'max']);
        expect(inferReasoningSpec('MiniMax-M3.1-Flash')?.param).toBe('reasoning.effort');
        expect(inferReasoningSpec('deepseek-v4-flash')?.mode).toBe('toggle');
        expect(inferReasoningSpec('qwen3.8-max')?.param).toBe('enable_thinking');
        expect(inferReasoningSpec('llama-3-70b')).toBeUndefined();
    });

    it('switching a model from toggle to effort replaces the on/off levels', async () => {
        mocked.list.mockResolvedValue([]);
        mocked.probe.mockResolvedValue(['glm-5.3']);
        render(<ModelProvidersModal onClose={() => {}} />);
        await waitFor(() => expect(mocked.list).toHaveBeenCalled());

        fireEvent.click(screen.getByText('自定义'));
        fireEvent.change(screen.getByPlaceholderText('https://api.example.com/v1'), { target: { value: 'http://127.0.0.1:8000/v1' } });
        fireEvent.click(screen.getByText('获取模型列表'));
        await waitFor(() => expect(mocked.probe).toHaveBeenCalled());

        // The probed GLM model starts as a toggle.
        fireEvent.click(screen.getByText(/思考强度/));
        const modeSelect = screen.getByDisplayValue('开关（可嵌套）') as HTMLSelectElement;
        fireEvent.change(modeSelect, { target: { value: 'effort' } });
        expect((screen.getByDisplayValue('low,medium,high,xhigh,max') as HTMLInputElement).value)
            .toBe('low,medium,high,xhigh,max');
    });
});
