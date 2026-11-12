import { Sparkles, Loader2 } from "lucide-react";
import { useState, useEffect } from "react";
import { zhCN } from "../../i18n/zh-CN";

const LoadingComponent = () => {
  const useCaseExamples = zhCN.app.useCases;
  const [useCaseIndex, setUseCaseIndex] = useState(
    Math.floor(Math.random() * useCaseExamples.length),
  );

  useEffect(() => {
    const interval = setInterval(() => {
      setUseCaseIndex(prev => (prev + 1) % useCaseExamples.length);
    }, 5000);
    return () => clearInterval(interval);
  }, [useCaseExamples.length]);

  return (
    <div className="h-screen max-h-screen overflow-hidden flex flex-col items-center justify-center bg-gradient-to-b from-white to-gray-50 p-4">
      <div className="flex flex-col items-center justify-center max-w-md w-full text-center">
        <div className="w-16 h-16 mb-6 flex items-center justify-center">
          <Loader2 className="w-16 h-16 text-[#FFDB58] animate-spin" />
        </div>

        <h2 className="text-2xl font-bold mb-2 flex items-center">
          {zhCN.app.loadingTitle} <Sparkles className="w-5 h-5 ml-2 text-[#FFDB58]" />
        </h2>

        <p className="text-gray-600 mb-8">{zhCN.app.loadingSubtitle}</p>

        <div className="neo-border bg-white p-4 rounded-lg mb-6 transition-all duration-500 ease-in-out">
          <div className="flex items-start">
            <div className="bg-[#FFDB58]/20 p-2 rounded-lg mr-3">
              <Sparkles className="w-4 h-4 text-gray-800" />
            </div>
            <div className="text-left">
              <h3 className="font-medium text-sm mb-1">{zhCN.app.loadingWhat}</h3>
              <p className="text-sm text-gray-600">{useCaseExamples[useCaseIndex]}</p>
            </div>
          </div>
        </div>
      </div>
    </div>
  );
};

export default LoadingComponent;
