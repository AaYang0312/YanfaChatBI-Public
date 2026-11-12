import { Send, Square, ChevronDown, Cpu } from 'lucide-react';
import { FormEvent, type ReactNode, useState, useEffect, useRef, useMemo } from 'react';
import analyticsService from '../../services/analyticsService';
import { LLMModel, CategorizedLLMModels } from '../../types/chat';
import { zhCN } from '../../i18n/zh-CN';
import { useSourceAccess } from '../../hooks/useSourceAccess';

// Note: Recommendations UI moved to ChatWindow

interface MessageInputProps {
    isConnected: boolean;
    onSendMessage: (content: string) => Promise<boolean>;
    isExpanded: boolean;
    isDisabled?: boolean;
    chatId?: string;
    userId?: string;
    userName?: string;
    isStreaming?: boolean;
    onCancelStream?: () => void;
    prefillText?: string;
    onConsumePrefill?: () => void;
    onModelChange?: (modelId: string) => void;
    selectedModel?: string;
    selectedReasoningLevel?: string;
    onReasoningChange?: (level: string) => void;
    generationOnly?: boolean;
    llmModels?: LLMModel[];
    isLoadingModels?: boolean;
    workflowControl: ReactNode;
}

export default function MessageInput({ 
    isConnected, 
    onSendMessage, 
    isExpanded, 
    isDisabled = false,
    chatId, 
    userId, 
    userName, 
    isStreaming, 
    onCancelStream, 
    prefillText, 
    onConsumePrefill,
    onModelChange,
    selectedModel,
    selectedReasoningLevel = '',
    onReasoningChange,
    generationOnly = false,
    llmModels = [],
    isLoadingModels = false,
    workflowControl,
}: MessageInputProps) {
    const [input, setInput] = useState('');
    const access = useSourceAccess(chatId);
    const [submitting, setSubmitting] = useState(false);
    const submissionRef = useRef(false);
    const activeChatRef = useRef(chatId);
    activeChatRef.current = chatId;
    const [isLoadingRecommendations] = useState(false);
    const [showModelDropdown, setShowModelDropdown] = useState(false);
    const modelDropdownRef = useRef<HTMLDivElement>(null);

    // Categorize models by provider
    const categorizedModels = useMemo(() => {
        const categorized: CategorizedLLMModels = {};
        
        llmModels.forEach(model => {
            if (!categorized[model.provider]) {
                categorized[model.provider] = [];
            }
            categorized[model.provider].push(model);
        });

        return categorized;
    }, [llmModels]);

    // Get provider order (Gemini first, then OpenAI, then others alphabetically)
    const providerOrder = useMemo(() => {
        // Provider order follows the catalogue order returned by the backend.
        return Object.keys(categorizedModels);
    }, [categorizedModels]);

    // Apply prefill from ChatWindow chips
    useEffect(() => {
        if (prefillText != null && prefillText !== '') {
            setInput(prefillText);
            onConsumePrefill?.();
        }
    }, [prefillText, onConsumePrefill]);

    // Close dropdown when clicking outside
    useEffect(() => {
        const handleClickOutside = (event: MouseEvent) => {
            if (modelDropdownRef.current && !modelDropdownRef.current.contains(event.target as Node)) {
                setShowModelDropdown(false);
            }
        };

        document.addEventListener('mousedown', handleClickOutside);
        return () => document.removeEventListener('mousedown', handleClickOutside);
    }, []);

    const handleSubmit = async (e: FormEvent) => {
        e.preventDefault();
        if (!input.trim() || !isConnected || isDisabled || isStreaming || submitting || submissionRef.current || !access.allowed) return;
        const draft = input;
        const targetChat = chatId;
        submissionRef.current = true;
        setSubmitting(true);
        try {
            if (chatId && userId && userName) analyticsService.trackMessageSubmit(chatId, draft.trim().length, userId, userName);
            const accepted = await onSendMessage(draft.trim());
            if (accepted && activeChatRef.current === targetChat) setInput(current => current === draft ? '' : current);
        } finally {
            submissionRef.current = false;
            if (activeChatRef.current === targetChat) setSubmitting(false);
        }
    };

    const handleModelChange = (modelId: string) => {
        onModelChange?.(modelId);
        setShowModelDropdown(false);
    };

    const currentModel = selectedModel && llmModels.find(m => m.id === selectedModel);
    const reasoningSpec = currentModel?.reasoning;
    const reasoningLevels = reasoningSpec?.levels ?? [];
    const effectiveReasoning = selectedReasoningLevel && reasoningLevels.includes(selectedReasoningLevel)
        ? selectedReasoningLevel
        : (reasoningSpec?.default_level ?? reasoningLevels[0] ?? '');
    const reasoningIndex = Math.max(0, reasoningLevels.indexOf(effectiveReasoning));
    const reasoningLabel = effectiveReasoning === reasoningSpec?.default_level
        ? `${effectiveReasoning}（默认）`
        : effectiveReasoning;

    // Get provider display name and icon
    const getProviderInfo = (provider: string) => {
        const match = llmModels.find(model => model.provider === provider);
        return {
            name: match?.providerName || provider,
            color: 'text-gray-700',
            bgColor: 'bg-gray-50',
            borderColor: 'border-gray-300',
        };
    };

    // Dice-driven recommendations removed; chips now rendered in ChatWindow

    // Chip click now handled in ChatWindow via prefill


    return (
        <div
            className={`
            fixed -bottom-1.5 left-0 right-0 p-4
            bg-white border-t-4 border-black 
            transition-all duration-300
            z-[10]
            ${isExpanded
                    ? `
                    [@media(min-width:1024px)_and_(max-width:1279px)]:ml-[20rem]
                    [@media(min-width:1280px)_and_(max-width:1439px)]:ml-[20rem]
                    [@media(min-width:1440px)_and_(max-width:1700px)]:ml-[18rem]
                    ml-2
                `
                    : 'md:left-[5rem]'
                }
        `}
        >
            <div className="max-w-5xl mx-auto chat-input-1440 relative">
                {!access.allowed && <p role="status" className="mb-2 text-sm">{access.reason}</p>}
                {generationOnly && (
                    <p role="note" className="mb-1 text-[11px] text-gray-600">{zhCN.connection.generationModeHint}</p>
                )}
                <div className="flex gap-2 md:gap-4 justify-center items-end">
                    {workflowControl}
                    <form onSubmit={handleSubmit} className="flex min-w-0 flex-1 items-end gap-2 md:gap-4">
                    <div className="relative min-w-0 flex-1">
                        {/* Input container with model selector inside */}
                        <div className="relative">
                            <textarea
                                value={input}
                                onChange={(e) => setInput(e.target.value)}
                                onKeyDown={(e) => {
                                    if (e.key === 'Enter' && !e.shiftKey) {
                                        e.preventDefault();
                                        void handleSubmit(e);
                                    }
                                }}
                                placeholder={
                                    isConnected
                                        ? zhCN.chat.placeholderConnected
                                        : zhCN.chat.placeholderDisconnected
                                }
                                className="
                        neo-input 
                        w-full
                        min-h-[52px]
                        resize-y
                        py-3
                        px-4
                        pr-20
                        leading-normal
                        whitespace-pre-wrap
                      "
                                rows={Math.min(
                                    Math.max(
                                        input.split('\n').length,
                                        Math.ceil(input.length / 50)
                                    ),
                                    5
                                )}
                                disabled={!isConnected}
                            />

                            {/* Model Selector - Inside textarea on right edge */}
                            <div ref={modelDropdownRef} className="absolute right-3 top-1/2 transform -translate-y-1/2">
                                <button
                                    type="button"
                                    onClick={() => setShowModelDropdown(!showModelDropdown)}
                                    disabled={isLoadingModels || llmModels.length === 0}
                                    className={`
                                        flex items-center gap-1.5 px-2 py-1.5
                                        bg-transparent hover:bg-gray-100 disabled:opacity-50
                                        transition-colors duration-200 rounded
                                        ${showModelDropdown ? 'bg-gray-100' : ''}
                                    `}
                                    title={currentModel ? `${zhCN.chat.selectedModel} ${currentModel.displayName}` : zhCN.chat.selectModel}
                                >
                                    {isLoadingModels ? (
                                        <span className="w-4 h-4 border-2 border-gray-400 border-t-transparent rounded-full animate-spin" />
                                    ) : (
                                        <>
                                        <div className='-mt-2 -mr-1 flex flex-row gap-1'>
                                            <Cpu className="w-4 h-4 text-green-700 flex-shrink-0" />
                                            <ChevronDown className={`w-3.5 h-3.5 mt-0.5 text-gray-600 flex-shrink-0 transition-transform ${showModelDropdown ? 'rotate-180' : ''}`} />
                                        </div>
                                        </>
                                    )}
                                </button>

                                {/* Model Dropdown Menu - positioned above textarea */}
                                {showModelDropdown && llmModels.length > 0 && (
                                    <div className="
                                        absolute -right-20 md:right-0 bottom-full mb-7 md:mb-3
                                        bg-white border-2 border-black
                                        shadow-[4px_4px_0px_rgba(0,0,0,1)]
                                        w-[340px] max-h-[380px] overflow-hidden flex flex-col
                                    ">
                                        {/* Dropdown Header */}
                                        <div className="px-4 py-3 border-b-2 border-black bg-gray-100">
                                            <h3 className="text-sm font-bold uppercase tracking-widest text-black">
                                                {zhCN.chat.selectModel}
                                            </h3>
                                            <p className="text-xs text-gray-700 mt-1">
                                                {llmModels.length} {zhCN.chat.modelsAvailable}
                                            </p>
                                        </div>

                                        {/* Models List */}
                                        <div className="overflow-y-auto flex-1 bg-white">
                                            {/* Show Selected Model at Top */}
                                            {currentModel && (
                                                <>
                                                    <button
                                                        type="button"
                                                        onClick={() => handleModelChange(currentModel.id)}
                                                        className="
                                                            w-full text-left px-4 py-2.5
                                                            border-b-2 border-green-600
                                                            bg-green-100 hover:bg-green-200
                                                            transition-colors duration-150
                                                            flex items-start justify-between gap-3
                                                        "
                                                    >
                                                        <div className="flex-1 min-w-0">
                                                            <span className="font-bold text-sm text-green-900">
                                                                {currentModel.displayName}
                                                            </span>
                                                        </div>
                                                        <div className="flex-shrink-0 pt-1">
                                                            <div className="w-5 h-5 bg-green-500 rounded-full flex items-center justify-center border border-green-600">
                                                                <svg className="w-3 h-3 text-white" fill="currentColor" viewBox="0 0 20 20">
                                                                    <path fillRule="evenodd" d="M16.707 5.293a1 1 0 010 1.414l-8 8a1 1 0 01-1.414 0l-4-4a1 1 0 011.414-1.414L8 12.586l7.293-7.293a1 1 0 011.414 0z" clipRule="evenodd" />
                                                                </svg>
                                                            </div>
                                                        </div>
                                                    </button>
                                                </>
                                            )}

                                            {providerOrder.map((provider, providerIndex) => {
                                                const models = categorizedModels[provider];
                                                const providerInfo = getProviderInfo(provider);
                                                
                                                return (
                                                    <div key={provider}>
                                                        {/* Provider Section Header */}
                                                        {providerIndex > 0 && <div className="border-t border-gray-200" />}
                                                        <div className="px-4 py-3 border-b border-gray-200 bg-gray-50">
                                                            <h4 className="text-xs font-bold uppercase tracking-wider text-black">
                                                                {providerInfo.name}
                                                            </h4>
                                                        </div>

                                                        {/* Models for this provider */}
                                                        {models.map((model, modelIndex) => (
                                                            <button
                                                                key={model.id}
                                                                type="button"
                                                                onClick={() => handleModelChange(model.id)}
                                                                className={`
                                                                    w-full text-left px-4 py-2.5
                                                                    border-b border-gray-200
                                                                    transition-colors duration-150
                                                                    flex items-start justify-between gap-3
                                                                    ${modelIndex === models.length - 1 && providerIndex === providerOrder.length - 1 ? 'last:border-b-0' : ''}
                                                                    ${selectedModel === model.id 
                                                                        ? 'bg-white' 
                                                                        : 'bg-transparent hover:bg-gray-200'
                                                                    }
                                                                `}
                                                            >
                                                                <div className="flex-1 min-w-0">
                                                                    <span className={`${selectedModel === model.id ? 'font-bold' : 'font-medium'} text-sm text-black`}>
                                                                        {model.displayName}
                                                                    </span>
                                                                </div>

                                                                {/* Active Indicator - Tick */}
                                                                {selectedModel === model.id && (
                                                                    <div className="flex-shrink-0 pt-1">
                                                                        <div className="w-5 h-5 bg-green-500 rounded-full flex items-center justify-center border border-green-700">
                                                                            <svg className="w-3 h-3 text-white" fill="currentColor" viewBox="0 0 20 20">
                                                                                <path fillRule="evenodd" d="M16.707 5.293a1 1 0 010 1.414l-8 8a1 1 0 01-1.414 0l-4-4a1 1 0 011.414-1.414L8 12.586l7.293-7.293a1 1 0 011.414 0z" clipRule="evenodd" />
                                                                            </svg>
                                                                        </div>
                                                                    </div>
                                                                )}
                                                            </button>
                                                        ))}
                                                    </div>
                                                );
                                            })}
                                        </div>
                                        {reasoningSpec && reasoningLevels.length > 0 && (
                                            <div className="border-t-2 border-black px-4 py-3 bg-white">
                                                <div className="flex items-center justify-between mb-1">
                                                    <span className="text-[10px] font-bold uppercase tracking-widest text-black">思考强度</span>
                                                    <span className="text-xs font-semibold text-black">{reasoningLabel}</span>
                                                </div>
                                                <input
                                                    type="range"
                                                    min={0}
                                                    max={reasoningLevels.length - 1}
                                                    step={1}
                                                    value={reasoningIndex}
                                                    onChange={event => onReasoningChange?.(reasoningLevels[Number(event.target.value)])}
                                                    className="w-full h-2 cursor-pointer accent-black"
                                                    aria-label="思考强度"
                                                    aria-valuetext={reasoningLabel}
                                                />
                                                <div className="flex justify-between text-[10px] text-gray-600 mt-0.5">
                                                    <span>{reasoningLevels[0]}</span>
                                                    <span>{reasoningLevels[reasoningLevels.length - 1]}</span>
                                                </div>
                                            </div>
                                        )}
                                    </div>
                                )}
                            </div>
                        </div>
                    </div>

                    <button
                        type={isStreaming ? "button" : "submit"}
                        onClick={isStreaming ? () => { onCancelStream?.(); } : undefined}
                        className={`neo-button px-4 md:px-6 self-end mb-1.5`}
                        disabled={(!isConnected || isLoadingRecommendations || isDisabled || submitting || !access.allowed) && !isStreaming}
                        title={isStreaming ? zhCN.chat.cancelRequest : access.reason || zhCN.chat.sendMessage}
                    >
                        {isStreaming ? (
                            <Square className="w-6 h-8 fill-current" />
                        ) : (
                            <Send className="w-6 h-8" />
                        )}
                    </button>
                    </form>
                </div>
            </div>
        </div>
    );
}