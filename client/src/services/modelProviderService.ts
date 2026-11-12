import axios from './axiosConfig';
import type { ProviderModel } from '../types/chat';

const API_URL = import.meta.env.VITE_API_URL;

export interface ProviderView {
    id: string;
    name: string;
    slug: string;
    base_url: string;
    api_key_masked: string;
    has_api_key: boolean;
    models: ProviderModel[];
    enabled: boolean;
    preset_id?: string;
    models_refreshed_at?: string;
    created_at?: string;
    updated_at?: string;
}

export interface ProviderInput {
    name: string;
    base_url: string;
    api_key?: string;
    models?: ProviderModel[];
    enabled?: boolean;
    preset_id?: string;
    refresh_models?: boolean;
    allow_empty_api_key?: boolean;
}

interface ProviderListResponse {
    success: boolean;
    data: { providers: ProviderView[]; count: number };
}

interface ProviderResponse {
    success: boolean;
    data: ProviderView;
}

interface ProbeResponse {
    success: boolean;
    data: { models: string[]; count: number };
}

const modelProviderService = {
    async list(): Promise<ProviderView[]> {
        const response = await axios.get<ProviderListResponse>(`${API_URL}/model-providers`);
        return response.data.data.providers;
    },

    async create(input: ProviderInput): Promise<ProviderView> {
        const response = await axios.post<ProviderResponse>(`${API_URL}/model-providers`, input);
        return response.data.data;
    },

    async update(id: string, input: ProviderInput): Promise<ProviderView> {
        const response = await axios.put<ProviderResponse>(`${API_URL}/model-providers/${id}`, input);
        return response.data.data;
    },

    async remove(id: string): Promise<void> {
        await axios.delete(`${API_URL}/model-providers/${id}`);
    },

    async probe(baseURL: string, apiKey: string): Promise<string[]> {
        const response = await axios.post<ProbeResponse>(`${API_URL}/model-providers/probe`, {
            base_url: baseURL,
            api_key: apiKey,
        });
        return response.data.data.models;
    },

    async refreshModels(id: string): Promise<ProviderView> {
        const response = await axios.post<ProviderResponse>(`${API_URL}/model-providers/${id}/refresh-models`);
        return response.data.data;
    },
};

export default modelProviderService;
