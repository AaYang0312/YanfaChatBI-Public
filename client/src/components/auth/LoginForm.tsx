import { AlertCircle, Boxes, KeyRound, Loader, UserRound } from 'lucide-react';
import React, { useState } from 'react';
import { LoginFormData } from '../../types/auth';
import analyticsService from '../../services/analyticsService';
import { productName, zhCN } from '../../i18n/zh-CN';

import ServiceErrorView from '../../i18n/ServiceErrorView';

interface LoginFormProps {
  onLogin: (data: LoginFormData) => Promise<void>;
  startupError?: string | null;
}

interface FormErrors {
  usernameOrEmail?: string;
  password?: string;
}

export default function LoginForm({ onLogin, startupError }: LoginFormProps) {
  const [errors, setErrors] = useState<FormErrors>({});
  const [touched, setTouched] = useState<Record<string, boolean>>({});
  const [isLoading, setIsLoading] = useState(false);
  const [formData, setFormData] = useState<LoginFormData>({
    usernameOrEmail: '',
    password: ''
  });
  const [formError, setFormError] = useState<unknown>(null);

  const validateUsernameOrEmail = (usernameOrEmail: string) => {
    if (!usernameOrEmail) return zhCN.auth.login.usernameRequired;
    if (usernameOrEmail.length < 3) return zhCN.auth.login.usernameMin;
    return '';
  };

  const validatePassword = (password: string) => {
    if (!password) return zhCN.auth.login.passwordRequired;
    if (password.length < 6) {
      return zhCN.auth.login.passwordMin;
    }
    return '';
  };

  const validateForm = () => {
    const newErrors: FormErrors = {};

    const usernameOrEmailError = validateUsernameOrEmail(formData.usernameOrEmail);
    if (usernameOrEmailError) newErrors.usernameOrEmail = usernameOrEmailError;

    const passwordError = validatePassword(formData.password);
    if (passwordError) newErrors.password = passwordError;

    setErrors(newErrors);
    return Object.keys(newErrors).length === 0;
  };

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setErrors({});
    setFormError(null);

    if (!validateForm()) return;

    setIsLoading(true);
    try {
      await onLogin(formData);
      analyticsService.trackEvent('login_attempt', { usernameOrEmail: formData.usernameOrEmail });
    } catch (error: unknown) {
      setFormError(error);
      analyticsService.trackEvent('login_error', { error: error instanceof Error ? error.message : undefined });
    } finally {
      setIsLoading(false);
    }
  };

  const handleChange = (e: React.ChangeEvent<HTMLInputElement>) => {
    const { name, value } = e.target;

    setFormData(prev => ({
      ...prev,
      [name]: value
    }));

    if (touched[name]) {
      if (name === 'usernameOrEmail') {
        const trimmedValue = value.trim();
        const error = validateUsernameOrEmail(trimmedValue);
        setErrors(prev => ({ ...prev, usernameOrEmail: error }));
      } else if (name === 'password') {
        const error = validatePassword(value);
        setErrors(prev => ({ ...prev, password: error }));
      }
    }
  };

  const handleBlur = (e: React.ChangeEvent<HTMLInputElement>) => {
    const { name, value } = e.target;
    setTouched(prev => ({ ...prev, [name]: true }));

    if (name === 'usernameOrEmail') {
      const trimmedValue = value.trim();
      if (trimmedValue !== value) {
        setFormData(prev => ({
          ...prev,
          [name]: trimmedValue
        }));
      }
      const error = validateUsernameOrEmail(trimmedValue);
      setErrors(prev => ({ ...prev, usernameOrEmail: error }));
    } else if (name === 'password') {
      const error = validatePassword(value);
      setErrors(prev => ({ ...prev, password: error }));
    }
  };

  return (
    <div className="min-h-screen bg-[#FFDB58]/20 flex items-center justify-center p-4">
      <div className="w-full max-w-md neo-border bg-white p-4 md:p-8">
        <h1 className="text-2xl md:text-3xl font-bold text-center mb-4 flex items-center justify-center gap-2">
          <Boxes className="w-10 h-10" />
          {productName}
        </h1>
        <p className="text-gray-600 text-center mb-8">
          {zhCN.auth.login.subtitle}
        </p>

        {startupError && !formError ? (
          <div className="mb-6 p-4 bg-red-50 border-2 border-red-500 rounded-lg text-red-600">
            <p className="font-medium">{startupError}</p>
          </div>
        ) : null}
        {formError ? (
          <div className="mb-6 p-4 bg-red-50 border-2 border-red-500 rounded-lg text-red-600">
            <ServiceErrorView error={formError} />
          </div>
        ) : null}

        <form onSubmit={handleSubmit} className="space-y-6">
          <div>
            <div className="relative">
              <UserRound className="absolute left-4 top-1/2 transform -translate-y-1/2 text-gray-500" />
              <input
                type="text"
                name="usernameOrEmail"
                placeholder={zhCN.auth.login.usernamePlaceholder}
                value={formData.usernameOrEmail}
                onChange={handleChange}
                onBlur={handleBlur}
                className={`neo-input pl-12 w-full ${errors.usernameOrEmail && touched.usernameOrEmail ? 'border-neo-error' : ''
                  }`}
                required
              />
            </div>
            {errors.usernameOrEmail && touched.usernameOrEmail && (
              <div className="flex items-center gap-1 mt-1 text-neo-error text-sm">
                <AlertCircle className="w-4 h-4" />
                <span>{errors.usernameOrEmail}</span>
              </div>
            )}
          </div>

          <div>
            <div className="relative">
              <KeyRound className="absolute left-4 top-1/2 transform -translate-y-1/2 text-gray-500" />
              <input
                type="password"
                name="password"
                placeholder={zhCN.auth.login.passwordPlaceholder}
                value={formData.password}
                onChange={handleChange}
                onBlur={handleBlur}
                className={`neo-input pl-12 w-full ${errors.password && touched.password ? 'border-neo-error' : ''
                  }`}
                required
              />
            </div>
            {errors.password && touched.password && (
              <div className="flex items-center gap-1 mt-1 text-neo-error text-sm">
                <AlertCircle className="w-4 h-4" />
                <span>{errors.password}</span>
              </div>
            )}
          </div>

          <button
            type="submit"
            className="neo-button w-full relative"
            disabled={isLoading}
          >
            {isLoading ? (
              <div className="flex items-center justify-center">
                <Loader className="w-4 h-4 animate-spin text-gray-400 mr-2" />
                {zhCN.auth.login.submitting}
              </div>
            ) : (
              zhCN.auth.login.submit
            )}
          </button>
        </form>
      </div>
    </div>
  );
}