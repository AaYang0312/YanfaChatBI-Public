import { useCallback, useEffect, useMemo, useState } from 'react';
import { ChevronDown, Download, Plus, RefreshCw, Server, Trash2, X } from 'lucide-react';
import modelProviderService, { ProviderInput, ProviderView } from '../../services/modelProviderService';
import type { ProviderModel, ReasoningSpec } from '../../types/chat';
import { inferReasoningSpec } from '../../utils/reasoning';

interface ModelProvidersModalProps {
    onClose: () => void;
    onChanged?: () => void;
}

interface Preset {
    preset_id: string;
    name: string;
    base_url: string;
    default_model: string;
    reasoning?: ReasoningSpec;
}

const PRESETS: Preset[] = [
    {
        preset_id: 'openai', name: 'OpenAI', base_url: 'https://api.openai.com/v1',
        default_model: 'gpt-6-astra',
        reasoning: { mode: 'effort', param: 'reasoning_effort', levels: ['low', 'medium', 'high', 'xhigh', 'max'], default_level: 'medium' },
    },
    {
        preset_id: 'deepseek', name: 'DeepSeek', base_url: 'https://api.deepseek.com/v1',
        default_model: 'deepseek-v4-flash',
        reasoning: { mode: 'toggle', param: 'thinking.type', levels: ['on', 'off'], default_level: 'on', on_value: 'enabled', off_value: 'disabled' },
    },
    {
        preset_id: 'zhipu', name: '智谱 GLM', base_url: 'https://open.bigmodel.cn/api/paas/v4',
        default_model: 'glm-5.3',
        reasoning: { mode: 'toggle', param: 'thinking.type', levels: ['on', 'off'], default_level: 'on', on_value: 'enabled', off_value: 'disabled' },
    },
    {
        preset_id: 'moonshot', name: 'Moonshot Kimi', base_url: 'https://api.moonshot.cn/v1',
        default_model: 'kimi-k3',
        reasoning: { mode: 'effort', param: 'reasoning_effort', levels: ['low', 'high', 'max'], default_level: 'max' },
    },
    {
        preset_id: 'minimax', name: 'MiniMax', base_url: 'https://api.minimax.io/v1',
        default_model: 'MiniMax-M3.1-Flash',
        reasoning: { mode: 'custom', param: 'reasoning.effort', levels: ['low', 'medium', 'high', 'xhigh', 'max'], default_level: 'max' },
    },
    {
        preset_id: 'qwen', name: '通义千问', base_url: 'https://dashscope.aliyuncs.com/compatible-mode/v1',
        default_model: 'qwen3.8-max',
        reasoning: { mode: 'toggle', param: 'enable_thinking', levels: ['on', 'off'], default_level: 'on', on_value: true, off_value: false },
    },
];

const emptyModel = (id: string, reasoning?: ReasoningSpec): ProviderModel => ({
    id,
    display_name: id,
    max_completion_tokens: 8192,
    context_limit: 131072,
    temperature: 1,
    reasoning: reasoning ?? inferReasoningSpec(id),
});

interface FormState {
    name: string;
    base_url: string;
    api_key: string;
    models: ProviderModel[];
    enabled: boolean;
    preset_id?: string;
    allow_empty_api_key: boolean;
}

const blankForm = (): FormState => ({ name: '', base_url: '', api_key: '', models: [], enabled: true, allow_empty_api_key: false });

export function ModelProvidersModal({ onClose, onChanged }: ModelProvidersModalProps) {
    const [providers, setProviders] = useState<ProviderView[]>([]);
    const [loading, setLoading] = useState(true);
    const [error, setError] = useState('');
    const [view, setView] = useState<'list' | 'form'>('list');
    const [editing, setEditing] = useState<ProviderView | null>(null);
    const [form, setForm] = useState<FormState>(blankForm());
    const [probeModels, setProbeModels] = useState<string[]>([]);
    const [probing, setProbing] = useState(false);
    const [saving, setSaving] = useState(false);
    const [deleteTarget, setDeleteTarget] = useState<ProviderView | null>(null);
    const [expandedReasoning, setExpandedReasoning] = useState<number | null>(null);

    const reload = useCallback(async () => {
        setLoading(true);
        try {
            const list = await modelProviderService.list();
            setProviders(list);
            setError('');
        } catch (err) {
            setError(errMessage(err, '加载模型设置失败'));
        } finally {
            setLoading(false);
        }
    }, []);

    useEffect(() => { void reload(); }, [reload]);

    const openCreate = (preset?: Preset) => {
        setEditing(null);
        setProbeModels([]);
        setExpandedReasoning(null);
        setForm(preset ? {
            name: preset.name,
            base_url: preset.base_url,
            api_key: '',
            models: [emptyModel(preset.default_model, preset.reasoning)],
            enabled: true,
            preset_id: preset.preset_id,
            allow_empty_api_key: false,
        } : blankForm());
        setView('form');
        setError('');
    };

    const openEdit = (provider: ProviderView) => {
        setEditing(provider);
        setProbeModels([]);
        setExpandedReasoning(null);
        setForm({
            name: provider.name,
            base_url: provider.base_url,
            api_key: '',
            // Suggest a spec for models saved before inference existed.
            models: provider.models.map(model => ({
                ...model,
                reasoning: model.reasoning ?? inferReasoningSpec(model.id),
            })),
            enabled: provider.enabled,
            preset_id: provider.preset_id,
            allow_empty_api_key: !provider.has_api_key,
        });
        setView('form');
        setError('');
    };

    const handleProbe = async () => {
        setProbing(true);
        setError('');
        try {
            const ids = await modelProviderService.probe(form.base_url, form.api_key);
            setProbeModels(ids);
            if (form.models.length === 0 && ids.length > 0) {
                setForm(current => ({ ...current, models: [emptyModel(ids[0])] }));
            }
        } catch (err) {
            setError(errMessage(err, '获取模型列表失败，可以手动填写模型'));
        } finally {
            setProbing(false);
        }
    };

    const toggleProbeModel = (id: string) => {
        setForm(current => {
            const exists = current.models.some(model => model.id === id);
            if (exists) {
                return { ...current, models: current.models.filter(model => model.id !== id) };
            }
            return { ...current, models: [...current.models, emptyModel(id)] };
        });
    };

    const updateModel = (index: number, patch: Partial<ProviderModel>) => {
        setForm(current => ({
            ...current,
            models: current.models.map((model, i) => (i === index ? { ...model, ...patch } : model)),
        }));
    };

    const updateReasoning = (index: number, patch: Partial<ReasoningSpec> | null) => {
        setForm(current => ({
            ...current,
            models: current.models.map((model, i) => {
                if (i !== index) return model;
                if (patch === null) return { ...model, reasoning: undefined };
                const base: ReasoningSpec = model.reasoning ?? { mode: 'none' };
                const next: ReasoningSpec = { ...base, ...patch };
                if (next.mode === 'none') return { ...model, reasoning: undefined };
                const modeChanged = patch.mode !== undefined && patch.mode !== base.mode;
                if (modeChanged) {
                    if (next.mode === 'toggle') {
                        Object.assign(next, { param: 'thinking.type', levels: ['on', 'off'], default_level: 'on', on_value: 'enabled', off_value: 'disabled' });
                    } else if (next.mode === 'effort') {
                        Object.assign(next, { param: 'reasoning_effort', levels: ['low', 'medium', 'high', 'xhigh', 'max'], default_level: 'medium' });
                    } else if (next.mode === 'custom') {
                        Object.assign(next, { param: 'reasoning.effort', levels: ['low', 'medium', 'high', 'xhigh', 'max'], default_level: 'medium' });
                    }
                }
                if (!next.levels || next.levels.length === 0) {
                    next.levels = next.mode === 'toggle' ? ['on', 'off'] : ['low', 'medium', 'high'];
                    next.param = next.param || (next.mode === 'toggle' ? 'thinking.type' : 'reasoning_effort');
                    next.default_level = next.default_level || next.levels[0];
                }
                return { ...model, reasoning: next };
            }),
        }));
    };

    const handleSave = async () => {
        setSaving(true);
        setError('');
        try {
            const payload: ProviderInput = {
                name: form.name.trim(),
                base_url: form.base_url.trim(),
                api_key: form.api_key,
                models: form.models,
                enabled: form.enabled,
                preset_id: form.preset_id,
                allow_empty_api_key: form.allow_empty_api_key,
            };
            if (editing) {
                await modelProviderService.update(editing.id, payload);
            } else {
                await modelProviderService.create(payload);
            }
            await reload();
            setView('list');
            onChanged?.();
        } catch (err) {
            setError(errMessage(err, '保存失败'));
        } finally {
            setSaving(false);
        }
    };

    const handleRefreshModels = async (provider: ProviderView) => {
        setError('');
        try {
            await modelProviderService.refreshModels(provider.id);
            await reload();
            onChanged?.();
        } catch (err) {
            setError(errMessage(err, '刷新模型列表失败'));
        }
    };

    const handleDelete = async () => {
        if (!deleteTarget) return;
        try {
            await modelProviderService.remove(deleteTarget.id);
            setDeleteTarget(null);
            await reload();
            onChanged?.();
        } catch (err) {
            setError(errMessage(err, '删除失败'));
        }
    };

    const selectedIds = useMemo(() => new Set(form.models.map(model => model.id)), [form.models]);

    return (
        <div className="fixed inset-0 bg-black/50 backdrop-blur-sm flex items-center justify-center p-4 z-[210]">
            <div className="bg-white border-2 border-black shadow-[4px_4px_0px_rgba(0,0,0,1)] w-full max-w-3xl max-h-[85vh] flex flex-col">
                <div className="px-5 py-4 border-b-2 border-black bg-gray-100 flex items-center justify-between">
                    <div className="flex items-center gap-2">
                        <Server className="w-5 h-5" />
                        <h2 className="text-sm font-bold uppercase tracking-widest">
                            {view === 'list' ? '模型设置' : editing ? '编辑模型提供商' : '添加模型提供商'}
                        </h2>
                    </div>
                    <button type="button" onClick={onClose} className="p-1 hover:bg-gray-200 rounded" aria-label="关闭">
                        <X className="w-5 h-5" />
                    </button>
                </div>

                <div className="flex-1 overflow-y-auto px-5 py-4 space-y-4">
                    {error && (
                        <div className="border-2 border-red-600 bg-red-50 text-red-700 text-sm px-3 py-2">{error}</div>
                    )}

                    {view === 'list' && (
                        <>
                            {loading ? (
                                <p className="text-sm text-gray-600">加载中…</p>
                            ) : providers.length === 0 ? (
                                <p className="text-sm text-gray-600">尚未配置模型提供商。添加一个开始使用。</p>
                            ) : (
                                <div className="space-y-2">
                                    {providers.map(provider => (
                                        <div key={provider.id} className="border-2 border-black px-3 py-2 flex items-center justify-between gap-3">
                                            <div className="min-w-0">
                                                <div className="font-bold text-sm flex items-center gap-2">
                                                    {provider.name}
                                                    <span className="text-[10px] font-medium uppercase tracking-wide border border-black px-1">{provider.slug}</span>
                                                    {!provider.enabled && <span className="text-xs text-gray-500">已停用</span>}
                                                </div>
                                                <div className="text-xs text-gray-600 truncate">{provider.base_url}</div>
                                                <div className="text-xs text-gray-600">
                                                    {provider.models.length} 个模型 · {provider.has_api_key ? provider.api_key_masked : '无密钥（本地端点）'}
                                                    {provider.models_refreshed_at ? ` · 刷新于 ${new Date(provider.models_refreshed_at).toLocaleString()}` : ''}
                                                </div>
                                            </div>
                                            <div className="flex items-center gap-1 flex-shrink-0">
                                                <button type="button" onClick={() => void handleRefreshModels(provider)} className="p-1.5 hover:bg-gray-100 rounded" title="刷新模型列表">
                                                    <RefreshCw className="w-4 h-4" />
                                                </button>
                                                <button type="button" onClick={() => openEdit(provider)} className="px-2 py-1 text-xs font-semibold border-2 border-black hover:bg-gray-100">编辑</button>
                                                <button type="button" onClick={() => setDeleteTarget(provider)} className="p-1.5 hover:bg-red-50 text-red-600 rounded" title="删除">
                                                    <Trash2 className="w-4 h-4" />
                                                </button>
                                            </div>
                                        </div>
                                    ))}
                                </div>
                            )}

                            <div>
                                <h3 className="text-xs font-bold uppercase tracking-widest mb-2">添加提供商</h3>
                                <div className="grid grid-cols-2 md:grid-cols-3 gap-2">
                                    {PRESETS.map(preset => (
                                        <button
                                            key={preset.preset_id}
                                            type="button"
                                            onClick={() => openCreate(preset)}
                                            className="border-2 border-black px-3 py-2 text-left hover:bg-gray-100"
                                        >
                                            <div className="font-bold text-sm">{preset.name}</div>
                                            <div className="text-[11px] text-gray-600 truncate">{preset.default_model}</div>
                                        </button>
                                    ))}
                                    <button
                                        type="button"
                                        onClick={() => openCreate()}
                                        className="border-2 border-dashed border-black px-3 py-2 text-left hover:bg-gray-100 flex items-center gap-2"
                                    >
                                        <Plus className="w-4 h-4" />
                                        <span className="font-bold text-sm">自定义</span>
                                    </button>
                                </div>
                            </div>
                        </>
                    )}

                    {view === 'form' && (
                        <div className="space-y-4">
                            <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
                                <label className="block">
                                    <span className="block text-xs font-bold uppercase tracking-widest mb-1">名称</span>
                                    <input className="neo-input w-full" value={form.name} onChange={event => setForm(current => ({ ...current, name: event.target.value }))} placeholder="例如 DeepSeek" />
                                </label>
                                <label className="block">
                                    <span className="block text-xs font-bold uppercase tracking-widest mb-1">Base URL</span>
                                    <input className="neo-input w-full" value={form.base_url} onChange={event => setForm(current => ({ ...current, base_url: event.target.value }))} placeholder="https://api.example.com/v1" />
                                </label>
                            </div>

                            <label className="block">
                                <span className="block text-xs font-bold uppercase tracking-widest mb-1">API Key</span>
                                <input
                                    className="neo-input w-full"
                                    type="password"
                                    value={form.api_key}
                                    onChange={event => setForm(current => ({ ...current, api_key: event.target.value }))}
                                    placeholder={editing?.has_api_key ? `已保存：${editing.api_key_masked}（留空不修改）` : '填写后获取模型列表'}
                                />
                                <label className="mt-1 flex items-center gap-2 text-xs text-gray-700">
                                    <input
                                        type="checkbox"
                                        checked={form.allow_empty_api_key}
                                        onChange={event => setForm(current => ({ ...current, allow_empty_api_key: event.target.checked }))}
                                    />
                                    该本地端点无需鉴权
                                </label>
                            </label>

                            <div className="flex items-center gap-2">
                                <button
                                    type="button"
                                    onClick={() => void handleProbe()}
                                    disabled={probing || !form.base_url}
                                    className="flex items-center gap-2 px-3 py-1.5 text-sm font-semibold border-2 border-black hover:bg-gray-100 disabled:opacity-50"
                                >
                                    <Download className="w-4 h-4" />
                                    {probing ? '获取中…' : '获取模型列表'}
                                </button>
                                <span className="text-xs text-gray-600">拉取 {form.base_url || '端点'}/models</span>
                            </div>

                            {probeModels.length > 0 && (
                                <div className="border-2 border-black max-h-40 overflow-y-auto">
                                    {probeModels.map(id => (
                                        <label key={id} className="flex items-center gap-2 px-3 py-1.5 text-sm hover:bg-gray-50 border-b border-gray-200 last:border-b-0">
                                            <input type="checkbox" checked={selectedIds.has(id)} onChange={() => toggleProbeModel(id)} />
                                            <span className="font-mono text-xs">{id}</span>
                                        </label>
                                    ))}
                                </div>
                            )}

                            <div>
                                <span className="block text-xs font-bold uppercase tracking-widest mb-1">模型列表（{form.models.length}）</span>
                                <div className="space-y-2">
                                    {form.models.map((model, index) => (
                                        <div key={`${model.id}-${index}`} className="border-2 border-black">
                                            <div className="flex items-center gap-2 px-3 py-2">
                                                <span className="font-mono text-xs flex-1 truncate">{model.id}</span>
                                                <input
                                                    className="neo-input text-xs w-40"
                                                    value={model.display_name}
                                                    onChange={event => updateModel(index, { display_name: event.target.value })}
                                                    placeholder="显示名"
                                                />
                                                <button
                                                    type="button"
                                                    onClick={() => setExpandedReasoning(expandedReasoning === index ? null : index)}
                                                    className="flex items-center gap-1 px-2 py-1 text-xs font-semibold border-2 border-black hover:bg-gray-100"
                                                >
                                                    {model.reasoning && model.reasoning.mode !== 'none'
                                                        ? `思考强度 · ${(model.reasoning.levels ?? []).join('/')}`
                                                        : '思考强度'}
                                                    <ChevronDown className={`w-3 h-3 transition-transform ${expandedReasoning === index ? 'rotate-180' : ''}`} />
                                                </button>
                                                <button type="button" onClick={() => setForm(current => ({ ...current, models: current.models.filter((_, i) => i !== index) }))} className="p-1 text-red-600 hover:bg-red-50 rounded">
                                                    <Trash2 className="w-4 h-4" />
                                                </button>
                                            </div>
                                            {expandedReasoning === index && (
                                                <div className="border-t-2 border-black bg-gray-50 px-3 py-2 grid grid-cols-2 gap-2">
                                                    <label className="block">
                                                        <span className="block text-[11px] font-bold uppercase tracking-widest mb-1">模式</span>
                                                        <select
                                                            className="neo-input w-full text-xs"
                                                            value={model.reasoning?.mode ?? 'none'}
                                                            onChange={event => updateReasoning(index, { mode: event.target.value as ReasoningSpec['mode'] })}
                                                        >
                                                            <option value="none">不支持</option>
                                                            <option value="effort">档位（顶层字段）</option>
                                                            <option value="toggle">开关（可嵌套）</option>
                                                            <option value="custom">自定义档位（可嵌套）</option>
                                                        </select>
                                                    </label>
                                                    {model.reasoning && model.reasoning.mode !== 'none' && (
                                                        <>
                                                            <label className="block">
                                                                <span className="block text-[11px] font-bold uppercase tracking-widest mb-1">参数名</span>
                                                                <input
                                                                    className="neo-input w-full text-xs font-mono"
                                                                    value={model.reasoning.param ?? ''}
                                                                    onChange={event => updateReasoning(index, { param: event.target.value })}
                                                                    placeholder="reasoning_effort 或 thinking.type"
                                                                />
                                                            </label>
                                                            <label className="block">
                                                                <span className="block text-[11px] font-bold uppercase tracking-widest mb-1">档位（逗号分隔）</span>
                                                                <input
                                                                    className="neo-input w-full text-xs font-mono"
                                                                    value={(model.reasoning.levels ?? []).join(',')}
                                                                    onChange={event => updateReasoning(index, { levels: event.target.value.split(',').map(item => item.trim()).filter(Boolean) })}
                                                                />
                                                            </label>
                                                            <label className="block">
                                                                <span className="block text-[11px] font-bold uppercase tracking-widest mb-1">默认档位</span>
                                                                <input
                                                                    className="neo-input w-full text-xs font-mono"
                                                                    value={model.reasoning.default_level ?? ''}
                                                                    onChange={event => updateReasoning(index, { default_level: event.target.value })}
                                                                />
                                                            </label>
                                                        </>
                                                    )}
                                                </div>
                                            )}
                                        </div>
                                    ))}
                                    {form.models.length === 0 && (
                                        <p className="text-sm text-gray-600">还没有模型。用「获取模型列表」选择，或手动添加。</p>
                                    )}
                                </div>
                                <button
                                    type="button"
                                    onClick={() => setForm(current => ({ ...current, models: [...current.models, emptyModel(`model-${current.models.length + 1}`)] }))}
                                    className="mt-2 flex items-center gap-1 px-2 py-1 text-xs font-semibold border-2 border-dashed border-black hover:bg-gray-100"
                                >
                                    <Plus className="w-3 h-3" /> 手动添加模型
                                </button>
                            </div>

                            <label className="flex items-center gap-2 text-sm">
                                <input type="checkbox" checked={form.enabled} onChange={event => setForm(current => ({ ...current, enabled: event.target.checked }))} />
                                启用该提供商
                            </label>
                        </div>
                    )}
                </div>

                <div className="px-5 py-3 border-t-2 border-black flex items-center justify-between">
                    {view === 'form' ? (
                        <>
                            <button type="button" onClick={() => { setView('list'); setError(''); }} className="px-3 py-1.5 text-sm font-semibold border-2 border-black hover:bg-gray-100">返回</button>
                            <button type="button" onClick={() => void handleSave()} disabled={saving} className="px-4 py-1.5 text-sm font-bold bg-black text-white border-2 border-black hover:bg-gray-800 disabled:opacity-50">
                                {saving ? '保存中…' : '保存'}
                            </button>
                        </>
                    ) : (
                        <button type="button" onClick={onClose} className="px-3 py-1.5 text-sm font-semibold border-2 border-black hover:bg-gray-100 ml-auto">关闭</button>
                    )}
                </div>
            </div>

            {deleteTarget && (
                <div className="fixed inset-0 bg-black/50 backdrop-blur-sm flex items-center justify-center p-4 z-[220]">
                    <div className="bg-white border-2 border-black shadow-[4px_4px_0px_rgba(0,0,0,1)] w-full max-w-md p-5">
                        <h3 className="text-sm font-bold uppercase tracking-widest mb-2">删除模型提供商</h3>
                        <p className="text-sm text-gray-700 mb-4">
                            确定删除「{deleteTarget.name}」？引用其模型的聊天会回退到默认模型。
                        </p>
                        <div className="flex justify-end gap-2">
                            <button type="button" onClick={() => setDeleteTarget(null)} className="px-3 py-1.5 text-sm font-semibold border-2 border-black hover:bg-gray-100">取消</button>
                            <button type="button" onClick={() => void handleDelete()} className="px-3 py-1.5 text-sm font-bold bg-red-600 text-white border-2 border-black hover:bg-red-700">删除</button>
                        </div>
                    </div>
                </div>
            )}
        </div>
    );
}

function errMessage(error: unknown, fallback: string): string {
    const response = (error as { response?: { data?: { error?: string } } }).response;
    if (response?.data?.error) return response.data.error;
    if (error instanceof Error && error.message) return `${fallback}：${error.message}`;
    return fallback;
}

export default ModelProvidersModal;
