import axios from 'axios';
import { getErrorMessage } from '../utils/message';


// Define API error response type
export interface APIErrorResponse {
    success: boolean;
    error: string;
}

// Create custom error class
export class APIError extends Error {
    constructor(message: string) {
        super(message);
        this.name = 'APIError';
    }
}

// Set default configs for all requests
axios.defaults.headers.common['Content-Type'] = 'application/json';
axios.defaults.headers.common['Accept'] = 'application/json';

// Add request interceptor
axios.interceptors.request.use(
    config => {
        const token = localStorage.getItem('token');
        if (token) {
            config.headers.set('Authorization', `Bearer ${token}`);
            config.headers.set('Content-Type', 'application/json');
            config.headers.set('Accept', 'application/json');
        }
        // Add this for all requests
        config.withCredentials = false; // Set to false since we're using Bearer token
        console.log('Starting Request:', {
            url: config.url,
            method: config.method,
            headers: config.headers
        });
        return config;
    },
    (error: unknown) => {
        const response = axios.isAxiosError<APIErrorResponse>(error) ? error.response : undefined;
        console.error('API Request Error:', {
            status: response?.status,
            data: response?.data,
            message: error instanceof Error ? error.message : undefined
        });
        return Promise.reject(new APIError(getErrorMessage(error, 'Request failed')));
    }
);

export default axios;